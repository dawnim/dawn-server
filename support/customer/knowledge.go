package customer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/prismgo/framework/encryption"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Evidence struct {
	models.KnowledgeEntry
	Score float64 `json:"score"`
}
type Retriever func(context.Context, uint, string) ([]Evidence, error)

var knowledgeWords = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9]*|[\p{Han}]+|[0-9]+`)

// knowledgeTerms保留英文标识，中文使用二字切分，不依赖外部分词服务。
func knowledgeTerms(text string) []string {
	terms := []string{}
	for _, word := range knowledgeWords.FindAllString(strings.ToLower(text), -1) {
		runes := []rune(word)
		if !unicode.Is(unicode.Han, runes[0]) || len(runes) == 1 {
			terms = append(terms, word)
			continue
		}
		for i := 1; i < len(runes); i++ {
			term := string(runes[i-1 : i+1])
			if !slices.Contains([]string{"怎么", "如何", "什么", "一下", "这个", "那个", "我们", "你们", "咱们", "请问", "可以", "啥的"}, term) {
				terms = append(terms, term)
			}
		}
	}
	return terms
}

// RegisterRetriever 注册已有知识系统，必须按传入的appID限制数据范围。
func (m *Manager) RegisterRetriever(name string, retriever Retriever) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrievers[name] = retriever
}

// IndexSpec固定查询所需的模型、维度和连接信息；Credential保存加密后的向量凭证。
type IndexSpec struct {
	Credential string `json:"credential"`
	Profile    string `json:"profile"`
	Driver     string `json:"driver"`
	URL        string `json:"url"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	CreatedAt  int64  `json:"created_at"`
}

type vectorRecord struct {
	EntryID     uint   `gorm:"primaryKey"`
	AppID       uint   `gorm:"not null;index"`
	Version     uint   `gorm:"not null"`
	ContentHash string `gorm:"not null;size:64"`
	Embedding   []byte `gorm:"not null"`
}

func indexTable(profile string) (string, error) {
	value, err := hex.DecodeString(profile)
	if err != nil || len(value) != 16 {
		return "", ErrInvalid
	}
	return "knowledge_vectors_" + profile, nil
}

func (m *Manager) embed(ctx context.Context, spec IndexSpec, query bool, text, key string) ([]float32, error) {
	if strings.Contains(strings.ToLower(spec.Model), "e5") {
		if query {
			text = "query: " + text
		} else {
			text = "passage: " + text
		}
	}
	if query && strings.Contains(strings.ToLower(spec.Model), "qwen3") {
		text = "Instruct: Retrieve support documentation that answers the question\nQuery: " + text
	}
	client := openai.NewClient(option.WithAPIKey(key), option.WithBaseURL(spec.URL), option.WithMaxRetries(0))
	params := openai.EmbeddingNewParams{Model: openai.EmbeddingModel(spec.Model), Input: openai.EmbeddingNewParamsInputUnion{OfString: openai.String(text)}}
	if spec.Dimensions > 0 {
		params.Dimensions = openai.Int(int64(spec.Dimensions))
	}
	response, err := client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, errors.New("向量服务不可用")
	}
	if len(response.Data) != 1 {
		return nil, ErrInvalid
	}
	v := make([]float32, len(response.Data[0].Embedding))
	var norm float64
	for i, value := range response.Data[0].Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, ErrInvalid
		}
		v[i] = float32(value)
		norm += value * value
	}
	if norm == 0 || spec.Dimensions > 0 && len(v) != spec.Dimensions {
		return nil, ErrInvalid
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(norm))
	}
	return v, nil
}

// availableSource排除已删除的资料来源，独立问答不受影响。
func availableSource(db *gorm.DB) *gorm.DB {
	return db.Where("source_id = 0 OR EXISTS (SELECT 1 FROM knowledge_sources AS source WHERE source.id = knowledge_entries.source_id AND source.app_id = knowledge_entries.app_id AND source.deleted_at = 0)")
}

func (m *Manager) Search(ctx context.Context, appID uint, question string) ([]Evidence, error) {
	_, cfg, _, err := m.App(ctx, appID)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	custom := m.retrievers[cfg.IndexDriver]
	m.mu.RUnlock()
	if custom != nil {
		return custom(ctx, appID, question)
	}
	scores := map[uint]float64{}
	var candidateIDs []uint
	var nearest []struct {
		EntryID     uint
		Version     uint
		ContentHash string
		Distance    float64
	}
	if cfg.ActiveIndex != nil && cfg.ActiveIndex.Driver == "mariadb" {
		spec := *cfg.ActiveIndex
		table, e := indexTable(spec.Profile)
		if e != nil {
			return nil, e
		}
		credential, e := encryption.DecryptString(ctx, spec.Credential)
		var vector []float32
		if e == nil {
			vector, e = m.embed(ctx, spec, true, question, credential)
		}
		if e == nil {
			// 每个索引档案只属于一个应用，先由原生向量索引选候选，再检查当前内容版本。
			e = m.DB.WithContext(ctx).Table(table).Select("entry_id, version, content_hash, VEC_DISTANCE_COSINE(embedding, VEC_FromText(?)) AS distance", string(Raw(vector))).Order("distance").Limit(12).Scan(&nearest).Error
			if e == nil {
				candidateIDs = make([]uint, 0, len(nearest))
				for _, row := range nearest {
					candidateIDs = append(candidateIDs, row.EntryID)
					scores[row.EntryID] = 1 - row.Distance
				}
			}
		}
	}
	terms := knowledgeTerms(question)
	slices.Sort(terms)
	terms = slices.Compact(terms)
	terms = terms[:min(48, len(terms))]
	query := m.DB.WithContext(ctx).Scopes(availableSource).Where("app_id = ? AND status = ?", appID, "published")
	if candidateIDs != nil {
		// 向量候选与正文关键词取并集，不能让语义候选挡住精确API名称。
		lexical := m.DB.Where("id IN ?", candidateIDs)
		for _, term := range terms {
			pattern := "%" + term + "%"
			lexical = lexical.Or("LOWER(question) LIKE ? OR LOWER(keywords) LIKE ? OR LOWER(answer) LIKE ?", pattern, pattern, pattern)
		}
		query = query.Where(lexical)
	}
	var entries []models.KnowledgeEntry
	if err = query.Find(&entries).Error; err != nil {
		return nil, err
	}
	if len(entries) == 0 && candidateIDs != nil {
		if err = m.DB.WithContext(ctx).Scopes(availableSource).Where("app_id = ? AND status = ?", appID, "published").Find(&entries).Error; err != nil {
			return nil, err
		}
	}
	if len(entries) == 0 {
		return []Evidence{}, nil
	}
	current := make(map[uint]models.KnowledgeEntry, len(entries))
	for _, entry := range entries {
		current[entry.ID] = entry
	}
	if cfg.ActiveIndex != nil && cfg.ActiveIndex.Driver != "mariadb" {
		spec := *cfg.ActiveIndex
		table, e := indexTable(spec.Profile)
		if e != nil {
			return nil, e
		}
		credential, e := encryption.DecryptString(ctx, spec.Credential)
		var vector []float32
		if e == nil {
			vector, e = m.embed(ctx, spec, true, question, credential)
		}
		if e == nil {
			var vectors []vectorRecord
			e = m.DB.WithContext(ctx).Table(table).Where("app_id = ?", appID).Find(&vectors).Error
			if e == nil {
				for _, row := range vectors {
					if len(row.Embedding) != len(vector)*4 {
						continue
					}
					var score float64
					for i, value := range vector {
						score += float64(value) * float64(math.Float32frombits(binary.LittleEndian.Uint32(row.Embedding[i*4:])))
					}
					if entry, ok := current[row.EntryID]; ok && entry.Version == row.Version && row.ContentHash == TokenHash(entry.Question+"\n"+entry.Answer) {
						scores[row.EntryID] = score
					}

				}
			}
		}
		// 索引或模型暂不可用时使用关键词，绝不临时更换模型查询旧索引。
	}
	for _, row := range nearest {
		if entry, ok := current[row.EntryID]; !ok || entry.Version != row.Version || row.ContentHash != TokenHash(entry.Question+"\n"+entry.Answer) {
			delete(scores, row.EntryID)
		}
	}
	frequencies := make([]map[string]float64, len(entries))
	documentFrequency := map[string]int{}
	lengths := make([]float64, len(entries))
	var averageLength float64
	for i, entry := range entries {
		frequencies[i] = map[string]float64{}
		for _, field := range []struct {
			text   string
			weight float64
		}{{entry.Question, 3}, {entry.Keywords, 2}, {entry.Answer, 1}} {
			for _, term := range knowledgeTerms(field.text) {
				frequencies[i][term] += field.weight
				lengths[i]++
			}
		}
		averageLength += lengths[i]
		for _, term := range terms {
			if frequencies[i][term] > 0 {
				documentFrequency[term]++
			}
		}
	}
	averageLength = max(1, averageLength/float64(len(entries)))
	result := []Evidence{}
	for i, entry := range entries {
		score := float64(0)
		if scores[entry.ID] > 0.35 {
			score = scores[entry.ID] * 2
		}
		for _, term := range terms {
			frequency := frequencies[i][term]
			if frequency > 0 {
				idf := math.Log(1 + (float64(len(entries)-documentFrequency[term])+0.5)/(float64(documentFrequency[term])+0.5))
				score += idf * frequency * 2.2 / (frequency + 1.2*(0.25+0.75*lengths[i]/averageLength))
			}
		}
		if score > 0 {
			result = append(result, Evidence{KnowledgeEntry: entry, Score: score})
		}
	}
	slices.SortStableFunc(result, func(a, b Evidence) int {
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return 0
	})
	selected := make([]Evidence, 0, min(8, len(result)))
	counts := map[uint]int{}
	for _, entry := range result {
		if entry.SourceID != 0 && counts[entry.SourceID] >= 2 {
			continue
		}
		selected = append(selected, entry)
		counts[entry.SourceID]++
		if len(selected) == 8 {
			break
		}
	}
	// 补齐主要来源的关联章节，避免只命中介绍而漏掉同文档中的实际调用方法。
	expanded := map[uint]bool{}
	for _, parent := range slices.Clone(selected) {
		if parent.SourceID == 0 || expanded[parent.SourceID] {
			continue
		}
		expanded[parent.SourceID] = true
		added := 0
		for _, entry := range entries {
			if entry.SourceID == parent.SourceID && !slices.ContainsFunc(selected, func(current Evidence) bool { return current.ID == entry.ID }) {
				selected = append(selected, Evidence{KnowledgeEntry: entry, Score: parent.Score / 2})
				added++
				if added == 2 {
					break
				}
			}
		}
		if len(expanded) == 3 {
			break
		}
	}
	return selected, nil
}

// BuildIndex 新代际全部完成后才切换活动配置，模型与维度不混用。
func (m *Manager) BuildIndex(ctx context.Context, appID uint) error {
	app, cfg, secrets, err := m.App(ctx, appID)
	if err != nil {
		return err
	}
	if cfg.EmbeddingURL == "" || cfg.EmbeddingModel == "" {
		return ErrInvalid
	}
	spec := IndexSpec{Driver: cfg.IndexDriver, URL: cfg.EmbeddingURL, Model: cfg.EmbeddingModel, Dimensions: cfg.EmbeddingDimensions, CreatedAt: time.Now().UnixMilli()}
	spec.Credential, err = encryption.EncryptString(ctx, secrets.EmbeddingAPIKey)
	if err != nil {
		return err
	}
	if spec.Driver == "" {
		spec.Driver = "portable"
	}
	if spec.Driver != "portable" && spec.Driver != "mariadb" {
		return ErrInvalid
	}
	if spec.Driver == "mariadb" && m.DB.Dialector.Name() != "mysql" {
		return ErrInvalid
	}
	var entries []models.KnowledgeEntry
	if err = m.DB.WithContext(ctx).Scopes(availableSource).Where("app_id = ? AND status = ?", appID, "published").Find(&entries).Error; err != nil {
		return err
	}
	probe, err := m.embed(ctx, spec, false, "knowledge", secrets.EmbeddingAPIKey)
	if err != nil {
		return err
	}
	spec.Dimensions = len(probe)
	if spec.Dimensions < 1 || spec.Dimensions > 16383 {
		return ErrInvalid
	}
	sum := sha256.Sum256(Raw(map[string]any{"model": spec, "generation": rand.Text()}))
	spec.Profile = hex.EncodeToString(sum[:16])
	table, _ := indexTable(spec.Profile)
	if spec.Driver == "mariadb" {
		err = m.DB.WithContext(ctx).Exec(fmt.Sprintf("CREATE TABLE `%s` (entry_id BIGINT UNSIGNED PRIMARY KEY, app_id BIGINT UNSIGNED NOT NULL, version BIGINT UNSIGNED NOT NULL, content_hash CHAR(64) NOT NULL, embedding VECTOR(%d) NOT NULL, INDEX(app_id), VECTOR INDEX(embedding) DISTANCE=cosine) COMMENT='系统:知识向量索引表'", table, spec.Dimensions)).Error
	} else {
		err = m.DB.WithContext(ctx).Table(table).Migrator().CreateTable(&vectorRecord{})
	}
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = m.DB.WithContext(context.WithoutCancel(ctx)).Table(table).Migrator().DropTable(table)
		}
	}()
	for _, entry := range entries {
		v, e := m.embed(ctx, spec, false, entry.Question+"\n"+entry.Answer, secrets.EmbeddingAPIKey)
		if e != nil {
			return e
		}
		if spec.Driver == "mariadb" {
			e = m.DB.WithContext(ctx).Exec("INSERT INTO `"+table+"` (entry_id,app_id,version,content_hash,embedding) VALUES (?,?,?,?,VEC_FromText(?))", entry.ID, appID, entry.Version, TokenHash(entry.Question+"\n"+entry.Answer), string(Raw(v))).Error
		} else {
			b, e2 := binary.Append(nil, binary.LittleEndian, v)
			if e2 != nil {
				return e2
			}
			e = m.DB.WithContext(ctx).Table(table).Create(&vectorRecord{EntryID: entry.ID, AppID: appID, Version: entry.Version, ContentHash: TokenHash(entry.Question + "\n" + entry.Answer), Embedding: b}).Error
		}
		if e != nil {
			return e
		}
	}
	previousIndex := cfg.ActiveIndex
	cfg.ActiveIndex = &spec
	cfg.IndexProfile = spec.Profile
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 发布期间知识发生变化则保留旧索引，交由调用方重新构建。
		var current []models.KnowledgeEntry
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Scopes(availableSource).Select("id", "version").Where("app_id = ? AND status = ?", appID, "published").Order("id").Find(&current).Error; e != nil {
			return e
		}
		if len(current) != len(entries) {
			return ErrConflict
		}
		versions := map[uint]uint{}
		for _, entry := range entries {
			versions[entry.ID] = entry.Version
		}
		for _, entry := range current {
			if versions[entry.ID] != entry.Version {
				return ErrConflict
			}
		}
		result := tx.Model(&app).Where("revision = ?", app.Revision).Updates(map[string]any{"config": Raw(cfg), "revision": gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		if previousIndex != nil {
			if e := tx.Create(&models.AIRun{AppID: appID, Kind: "index_cleanup", Input: Raw(map[string]any{"profile": previousIndex.Profile}), AvailableAt: time.Now().Add(time.Hour).UnixMilli(), DeadlineAt: time.Now().Add(6 * time.Hour).UnixMilli()}).Error; e != nil {
				return e
			}
		}
		return nil
	})
	complete = err == nil
	return err
}

// ImportSource 从文本或公开URL生成待审核分块，不直接发布资料。
func (m *Manager) ImportSource(ctx context.Context, id uint) error {
	var source models.KnowledgeSource
	if err := m.DB.WithContext(ctx).First(&source, id).Error; err != nil {
		return err
	}
	text, err := FetchKnowledge(ctx, source.Kind, source.Content)
	if err != nil {
		return err
	}
	return m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.KnowledgeSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
			return err
		}
		if current.Content != source.Content || current.Kind != source.Kind || current.Name != source.Name {
			return ErrConflict
		}
		hash := TokenHash("sections-v2\n" + source.Name + "\n" + text)
		if current.ContentHash == hash {
			return tx.Model(&source).Updates(map[string]any{"status": "completed", "error": ""}).Error
		}
		if err := tx.Model(&models.KnowledgeEntry{}).Where("source_id = ? AND status = ?", id, "published").Updates(map[string]any{"status": "disabled", "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ? AND status = ?", id, "draft").Delete(&models.KnowledgeEntry{}).Error; err != nil {
			return err
		}
		for _, entry := range SplitKnowledge(source.Name, text) {
			entry.AppID, entry.SourceID, entry.Status, entry.Location = source.AppID, id, "draft", source.Name
			if source.Kind == "url" {
				entry.Location = source.Content
			}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
		}
		return tx.Model(&source).Updates(map[string]any{"status": "completed", "content_hash": hash, "error": ""}).Error
	})
}

// FetchKnowledge 读取文本或公开网页，复用于预览生成与自动导入。
func FetchKnowledge(ctx context.Context, kind, content string) (string, error) {
	text := content
	if kind == "url" {
		u, err := url.Parse(text)
		if err != nil || u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
			return "", ErrInvalid
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, e := net.SplitHostPort(address)
			if e != nil {
				return nil, e
			}
			ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if e != nil {
				return nil, e
			}
			for _, ip := range ips {
				if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
					return nil, ErrForbidden
				}
			}
			if len(ips) == 0 {
				return nil, ErrInvalid
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		}
		client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, text, nil)
		response, e := client.Do(req)
		if e != nil {
			return "", errors.New("知识来源不可访问")
		}
		defer response.Body.Close()
		defer transport.CloseIdleConnections()
		if response.StatusCode != 200 {
			return "", errors.New("知识来源不可访问")
		}
		data, e := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
		if e != nil {
			return "", e
		}
		if len(data) > 4<<20 {
			return "", ErrInvalid
		}
		text = string(data)
		if strings.Contains(response.Header.Get("Content-Type"), "text/html") {
			document, e := html.Parse(bytes.NewReader(data))
			if e != nil {
				return "", e
			}
			var content strings.Builder
			var walk func(*html.Node)
			walk = func(node *html.Node) {
				if node.Type == html.ElementNode && slices.Contains([]string{"script", "style", "noscript", "nav", "footer", "header"}, node.Data) {
					return
				}
				if node.Type == html.TextNode {
					if value := strings.TrimSpace(node.Data); value != "" {
						content.WriteString(value)
						content.WriteByte('\n')
					}
				}
				if node.Type == html.ElementNode && slices.Contains([]string{"h1", "h2", "h3", "h4", "h5", "h6"}, node.Data) {
					content.WriteString("\n" + strings.Repeat("#", int(node.Data[1]-'0')) + " ")
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
			}
			walk(document)
			text = content.String()
		}
	}
	return text, nil
}
