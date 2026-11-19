package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/console"
)

// ModelCheckCommand在应用环境中诊断已保存的模型配置，不创建聊天或修改配置。
type ModelCheckCommand struct{}

func (*ModelCheckCommand) Definition() *console.Definition {
	return console.MustDefinition("dawn:model-check {app : 客服应用ID} {--queue : 通过真实队列检查模型与工具调用} {--json : 以JSON输出检查结果}", i18n.Text("", "测试客服模型连接"))
}
func (*ModelCheckCommand) Handle(command console.CommandContext) (resultErr error) {
	display := supervisor.NewDisplay(command.IO(), i18n.Locale(command.Context()), command.Input().OptionBool("json"))
	start := time.Now()
	if !display.JSON {
		display.Heading("测试客服模型连接")
		defer func() { display.Result(start, resultErr) }()
	}
	id, err := strconv.ParseUint(command.Input().Argument("app"), 10, 64)
	if err != nil {
		return customer.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(command.Context(), time.Minute)
	defer cancel()
	manager := customer.Resolve()
	var result map[string]any
	if command.Input().OptionBool("queue") {
		run := models.AIRun{AppID: uint(id), Kind: "probe", DeadlineAt: time.Now().Add(time.Minute).UnixMilli()}
		if err = manager.DB.WithContext(ctx).Create(&run).Error; err != nil {
			return err
		}
		if err = manager.DispatchPending(ctx); err != nil {
			return err
		}
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err = manager.DB.WithContext(ctx).First(&run, run.ID).Error; err != nil {
				return err
			}
			if run.Status == "completed" {
				err = json.Unmarshal(run.Output, &result)
				break
			}
			if run.Status == "failed" || run.Status == "cancelled" {
				return errors.New(run.Error)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	} else {
		result, err = manager.TestModel(ctx, uint(id))
	}
	if err != nil {
		return err
	}
	if display.JSON {
		if display.Options.Quiet || display.Options.Silent {
			return nil
		}
		return json.NewEncoder(display.Out).Encode(result)
	}
	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	rows := make([][]string, 0, len(keys))
	for _, key := range keys {
		value, _ := json.Marshal(result[key])
		rows = append(rows, []string{key, string(value)})
	}
	return command.IO().Table([]string{i18n.Text(display.Locale, "检查项"), i18n.Text(display.Locale, "结果")}, rows)
}
