package controllers

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/gin-gonic/gin"
	fs "github.com/prismgo/framework/contracts/filesystem"
	"github.com/prismgo/framework/logger"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type uploadInput struct {
	Name       string `json:"name" binding:"required,max=255"`
	MD5        string `json:"md5" binding:"required,len=32,hexadecimal"`
	SHA256     string `json:"sha256" binding:"required,len=64,hexadecimal"`
	Size       int64  `json:"size" binding:"required,gt=0"`
	CategoryID uint   `json:"category_id"`
}
type chunkUpload struct {
	uploadInput
	UserID    uint  `json:"user_id"`
	ChunkSize int64 `json:"chunk_size"`
}

func (a *Controller) AttachmentRoutes(g *gin.RouterGroup) {
	query := func(c *gin.Context) *gorm.DB {
		return a.DB.Model(&models.Attachment{}).Where("scope = ?", "admin")
	}
	g.POST("/attachments/move", middleware.Require(a.App, "dawn.attachments.update"), func(c *gin.Context) {
		var input struct {
			IDs        []uint `json:"ids" binding:"required,min=1,max=200"`
			CategoryID uint   `json:"category_id"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "参数格式错误")
			return
		}
		if input.CategoryID != 0 {
			var category models.Category
			if err := a.DB.First(&category, input.CategoryID).Error; err != nil {
				response.Report(c, err)
				return
			}
		}
		if err := query(c).Where("id IN ?", input.IDs).Update("category_id", input.CategoryID).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.GET("/attachments", middleware.Require(a.App, "dawn.attachments.list"), func(c *gin.Context) {
		db := query(c)
		if v := c.Query("name"); v != "" {
			db = db.Where("name LIKE ?", "%"+v+"%")
		}
		if v := c.Query("mime"); v != "" {
			db = db.Where("file_id IN (?)", a.DB.Model(&models.File{}).Select("id").Where("mime LIKE ?", v+"%"))
		}
		if v := c.Query("category_id"); v != "" {
			db = db.Where("category_id = ?", v)
		}
		var total int64
		if err := db.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		var rows []models.Attachment
		if err := db.Preload("File").Order(response.Order(c, "id", "name", "created_at")).Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		data := []gin.H{}
		for _, row := range rows {
			data = append(data, gin.H{"id": row.ID, "name": row.Name, "category_id": row.CategoryID, "created_by": row.CreatedBy, "created_at": row.CreatedAt, "size": row.File.Size, "mime": row.File.MIME, "md5": row.File.MD5, "url": fmt.Sprintf("%s/attachments/%d/content", a.Options.AdminPrefix, row.ID)})
		}
		response.Respond(c, gin.H{"data": data, "total": total, "current_page": page, "per_page": size})
	})
	g.POST("/attachments/check", middleware.Require(a.App, "dawn.attachments.create"), func(c *gin.Context) {
		var input uploadInput
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "文件摘要或大小格式错误")
			return
		}
		var file models.File
		if err := a.DB.Where("md5 = ? AND sha256 = ? AND size = ?", input.MD5, input.SHA256, input.Size).First(&file).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				response.Respond(c, gin.H{"exists": false})
				return
			}
			response.Report(c, err)
			return
		}

		if err := a.validateUpload(c, input, file.MIME); err != nil {
			response.Report(c, err, 422)
			return
		}
		disk, _, err := a.FileDisk(c.Request.Context(), &file.Location)
		if err != nil {
			response.Report(c, err)
			return
		}
		if closer, ok := disk.(io.Closer); ok {
			defer closer.Close()
		}
		exists, err := disk.Exists(c.Request.Context(), file.Path)
		if err != nil {
			response.Report(c, err)
			return
		}
		if !exists {
			response.Respond(c, gin.H{"exists": false})
			return
		}
		attachment, err := a.attachFile(c, input, file.ID)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"exists": true, "id": attachment.ID, "url": fmt.Sprintf("%s/attachments/%d/content", a.Options.AdminPrefix, attachment.ID)})
	})
	g.POST("/attachments/upload", middleware.Require(a.App, "dawn.attachments.create"), func(c *gin.Context) {
		limit, err := a.Setting(c.Request.Context(), "upload.max_size")
		if err != nil {
			response.Report(c, err)
			return
		}
		var size int64
		if err := json.Unmarshal(limit, &size); err != nil {
			response.Report(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, size+(1<<20))
		header, err := c.FormFile("file")
		if err != nil {
			response.Fail(c, 413, "文件读取失败或超过大小限制")
			return
		}
		defer func() {
			if c.Request.MultipartForm != nil {
				if err := c.Request.MultipartForm.RemoveAll(); err != nil {
					logger.Error("清理上传临时文件失败", map[string]any{"error": err})
				}
			}
		}()
		file, err := header.Open()
		if err != nil {
			response.Report(c, err)
			return
		}
		defer file.Close()
		category, _ := strconv.ParseUint(c.PostForm("category_id"), 10, 64)
		input := uploadInput{Name: header.Filename, MD5: c.PostForm("md5"), SHA256: c.PostForm("sha256"), Size: header.Size, CategoryID: uint(category)}
		attachment, err := a.saveUpload(c, file, input, size)
		if err != nil {
			response.Report(c, err, 422)
			return
		}
		response.Respond(c, gin.H{"id": attachment.ID, "url": fmt.Sprintf("%s/attachments/%d/content", a.Options.AdminPrefix, attachment.ID)})
	})
	g.GET("/attachments/:id/content", middleware.Require(a.App, ""), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var row models.Attachment
		if err := query(c).Preload("File").First(&row, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		a.serveAttachment(c, row)
	})
	g.PUT("/attachments/:id", middleware.Require(a.App, "dawn.attachments.update"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var input struct {
			Name       string `json:"name" binding:"required,max=255"`
			CategoryID uint   `json:"category_id"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "附件名称不能为空")
			return
		}
		result := query(c).Where("id = ?", id).Updates(map[string]any{"name": input.Name, "category_id": input.CategoryID})
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			response.Fail(c, 404, "附件不存在")
			return
		}
		response.Respond(c, nil)
	})
	g.DELETE("/attachments/:id", middleware.Require(a.App, "dawn.attachments.delete"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		result := query(c).Delete(&models.Attachment{}, id)
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			response.Fail(c, 404, "附件不存在")
			return
		}
		response.Respond(c, nil)
	})
	g.POST("/attachments/uploads", middleware.Require(a.App, "dawn.attachments.create"), func(c *gin.Context) {
		var input uploadInput
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "文件信息格式错误")
			return
		}
		limit, err := a.Setting(c.Request.Context(), "upload.max_size")
		if err != nil {
			response.Report(c, err)
			return
		}
		var maxSize int64
		if err := json.Unmarshal(limit, &maxSize); err != nil {
			response.Report(c, err)
			return
		}
		if input.Size > maxSize {
			response.Fail(c, 413, "文件超过大小限制")
			return
		}
		id := rand.Text()
		manifest := chunkUpload{uploadInput: input, UserID: middleware.Actor(c).User.ID, ChunkSize: 2 << 20}
		data, err := json.Marshal(manifest)
		if err != nil {
			response.Report(c, err)
			return
		}
		if err := a.Redis.Set(c.Request.Context(), a.Key(constants.TempUpload, id), data, constants.TempUpload.TTL).Err(); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"upload_id": id, "chunk_size": manifest.ChunkSize})
	})
	g.PUT("/attachments/uploads/:upload/:index", middleware.Require(a.App, "dawn.attachments.create"), func(c *gin.Context) {
		manifest, err := a.chunkManifest(c)
		if err != nil {
			response.Fail(c, 404, "上传会话不存在")
			return
		}
		index, err := strconv.ParseInt(c.Param("index"), 10, 64)
		chunks := (manifest.Size + manifest.ChunkSize - 1) / manifest.ChunkSize
		if err != nil || index < 0 || index >= chunks {
			response.Fail(c, 422, "分片编号无效")
			return
		}
		expected := min(manifest.ChunkSize, manifest.Size-index*manifest.ChunkSize)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, expected)
		data, err := io.ReadAll(c.Request.Body)
		if err != nil || int64(len(data)) != expected {
			response.Fail(c, 422, "分片长度错误")
			return
		}
		key := fmt.Sprintf("chunks/%s/%d", c.Param("upload"), index)
		if err := a.Disk.Put(c.Request.Context(), key, data, fs.PutOptions{ContentType: "application/octet-stream"}); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.POST("/attachments/uploads/:upload/complete", middleware.Require(a.App, "dawn.attachments.create"), func(c *gin.Context) {
		manifest, err := a.chunkManifest(c)
		if err != nil {
			response.Fail(c, 404, "上传会话不存在")
			return
		}
		readers := []io.Reader{}
		closers := []io.ReadCloser{}
		defer func() {
			for _, stream := range closers {
				if err := stream.Close(); err != nil {
					logger.Error("关闭分片失败", map[string]any{"error": err})
				}
			}
		}()
		for i := int64(0); i < (manifest.Size+manifest.ChunkSize-1)/manifest.ChunkSize; i++ {
			stream, err := a.Disk.ReadStream(c.Request.Context(), fmt.Sprintf("chunks/%s/%d", c.Param("upload"), i))
			if err != nil {
				response.Fail(c, 422, "分片不完整")
				return
			}
			readers = append(readers, stream)
			closers = append(closers, stream)
		}
		attachment, err := a.saveUpload(c, io.MultiReader(readers...), manifest.uploadInput, manifest.Size)
		if err != nil {
			response.Report(c, err, 422)
			return
		}
		if err := a.Redis.Del(c.Request.Context(), a.Key(constants.TempUpload, c.Param("upload"))).Err(); err != nil {
			response.Report(c, err)
			return
		}
		if err := a.Disk.DeleteDirectory(c.Request.Context(), "chunks/"+c.Param("upload")); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, gin.H{"id": attachment.ID, "url": fmt.Sprintf("%s/attachments/%d/content", a.Options.AdminPrefix, attachment.ID)})
	})
}
func (a *Controller) chunkManifest(c *gin.Context) (chunkUpload, error) {
	var manifest chunkUpload
	raw, err := a.Redis.Get(c.Request.Context(), a.Key(constants.TempUpload, c.Param("upload"))).Bytes()
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, err
	}
	if manifest.UserID != middleware.Actor(c).User.ID {
		return manifest, redis.Nil
	}
	return manifest, nil
}

// saveUpload 被普通上传和分片合并共用, 校验实际内容后通过配置的磁盘写入.
func (a *Controller) saveUpload(c *gin.Context, reader io.Reader, input uploadInput, limit int64) (models.Attachment, error) {
	var attachment models.Attachment
	temp, err := os.CreateTemp(a.Options.Storage, "upload-*")
	if err != nil {
		return attachment, err
	}
	defer func() {
		if err := temp.Close(); err != nil {
			logger.Error("关闭上传临时文件失败", map[string]any{"error": err})
		}
		if err := os.Remove(temp.Name()); err != nil {
			logger.Error("删除上传临时文件失败", map[string]any{"error": err})
		}
	}()
	md5Hash, shaHash := md5.New(), sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, md5Hash, shaHash), io.LimitReader(reader, limit+1))
	if err != nil {
		return attachment, err
	}
	if size > limit || size != input.Size {
		return attachment, i18n.Errorf("实际文件大小不匹配")
	}
	md5Value, shaValue := hex.EncodeToString(md5Hash.Sum(nil)), hex.EncodeToString(shaHash.Sum(nil))
	if (input.MD5 != "" && input.MD5 != md5Value) || (input.SHA256 != "" && input.SHA256 != shaValue) {
		return attachment, i18n.Errorf("文件摘要校验失败")
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return attachment, err
	}
	head := make([]byte, 512)
	n, err := temp.Read(head)
	if err != nil && err != io.EOF {
		return attachment, err
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return attachment, err
	}
	contentType := http.DetectContentType(head[:n])
	if err := a.validateUpload(c, input, contentType); err != nil {
		return attachment, err
	}
	var existing models.File
	if err := a.DB.Where("sha256 = ?", shaValue).Limit(1).Find(&existing).Error; err != nil {
		return attachment, err
	}
	if existing.ID != 0 {
		old, _, err := a.FileDisk(c.Request.Context(), &existing.Location)
		if err != nil {
			return attachment, err
		}
		if closer, ok := old.(io.Closer); ok {
			defer closer.Close()
		}
		exists, err := old.Exists(c.Request.Context(), existing.Path)
		if err != nil {
			return attachment, err
		}
		if exists {
			return a.attachFile(c, input, existing.ID)
		}
	}
	disk, location, err := a.FileDisk(c.Request.Context(), nil)
	if err != nil {
		return attachment, err
	}
	if closer, ok := disk.(io.Closer); ok {
		defer closer.Close()
	}
	key := "objects/" + shaValue[:2] + "/" + shaValue
	if err := disk.PutReader(c.Request.Context(), key, temp, fs.PutOptions{ContentType: contentType}); err != nil {
		return attachment, err
	}
	file := models.File{Location: location, MD5: md5Value, SHA256: shaValue, Size: size, Path: key, MIME: contentType}
	if err := a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "sha256"}}, DoUpdates: clause.AssignmentColumns([]string{"location", "path", "mime", "size"})}).Create(&file).Error; err != nil {
		return attachment, err
	}
	if err := a.DB.Where("sha256 = ?", shaValue).First(&file).Error; err != nil {
		return attachment, err
	}
	return a.attachFile(c, input, file.ID)
}

// validateUpload 同时约束实际上传和秒传复用。
func (a *Controller) validateUpload(c *gin.Context, input uploadInput, contentType string) error {
	raw, err := a.Setting(c.Request.Context(), "upload.max_size")
	if err != nil {
		return err
	}
	var maxSize int64
	if err := json.Unmarshal(raw, &maxSize); err != nil {
		return err
	}
	if input.Size > maxSize {
		return i18n.Errorf("文件超过大小限制")
	}
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(input.Name)), ".")
	allowed, err := a.Setting(c.Request.Context(), "upload.extensions")
	if err != nil {
		return err
	}
	var extensions string
	if err := json.Unmarshal(allowed, &extensions); err != nil {
		return err
	}
	if !slices.Contains(strings.Split(strings.ReplaceAll(extensions, " ", ""), ","), extension) {
		return i18n.Errorf("不支持该文件扩展名")
	}
	if strings.HasPrefix(contentType, "image/") {
		raw, err := a.Setting(c.Request.Context(), "upload.image_extensions")
		if err != nil {
			return err
		}
		var images string
		if err := json.Unmarshal(raw, &images); err != nil {
			return err
		}
		if !slices.Contains(strings.Split(strings.ReplaceAll(images, " ", ""), ","), extension) {
			return i18n.Errorf("不支持该文件扩展名")
		}
	}
	if input.CategoryID != 0 {
		if err := a.DB.First(&models.Category{}, input.CategoryID).Error; err != nil {
			return err
		}
	}
	return nil
}

func (a *Controller) serveAttachment(c *gin.Context, row models.Attachment) {
	disk, _, err := a.FileDisk(c.Request.Context(), &row.File.Location)
	if err != nil {
		response.Report(c, err)
		return
	}
	if closer, ok := disk.(io.Closer); ok {
		defer closer.Close()
	}
	stream, err := disk.ReadStream(c.Request.Context(), row.File.Path)
	if err != nil {
		response.Report(c, err)
		return
	}
	defer stream.Close()
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	disposition := "attachment"
	if row.File.MIME == "image/png" || row.File.MIME == "image/jpeg" || row.File.MIME == "image/gif" || row.File.MIME == "image/webp" {
		disposition = "inline"
	}
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": row.Name}))
	c.DataFromReader(200, row.File.Size, row.File.MIME, stream, nil)
}

// attachFile 保留后台库的唯一文件引用；重新上传已删除附件时恢复原记录。
func (a *Controller) attachFile(c *gin.Context, input uploadInput, fileID uint) (models.Attachment, error) {
	row := models.Attachment{Scope: "admin", Name: path.Base(strings.ReplaceAll(input.Name, "\\", "/")), FileID: fileID, CategoryID: input.CategoryID, CreatedBy: middleware.Actor(c).User.ID}
	err := a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}, {Name: "file_id"}}, DoUpdates: clause.Assignments(map[string]any{"deleted_at": 0})}).Create(&row).Error
	if err == nil {
		row.ID = 0
		err = a.DB.Where("scope = ? AND file_id = ?", "admin", fileID).First(&row).Error
	}
	return row, err
}
