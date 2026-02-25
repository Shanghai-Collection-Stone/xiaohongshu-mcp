package xiaohongshu

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// PublishVideoContent 发布视频内容
type PublishVideoContent struct {
	Title        string
	Content      string
	Tags         []string
	VideoPath    string
	Location     string
	ScheduleTime *time.Time // 定时发布时间，nil 表示立即发布
}

// NewPublishVideoAction 进入发布页并切换到"上传视频"
func NewPublishVideoAction(page *rod.Page) (*PublishAction, error) {
	pp := page.Timeout(300 * time.Second)

	if err := pp.Navigate(urlOfPublic); err != nil {
		return nil, errors.Wrap(err, "导航到发布页面失败")
	}

	// 使用 WaitLoad 代替 WaitIdle（更宽松）
	if err := pp.WaitLoad(); err != nil {
		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
	}
	time.Sleep(2 * time.Second)

	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
	}
	time.Sleep(1 * time.Second)

	if err := mustClickPublishTab(pp, "上传视频"); err != nil {
		return nil, errors.Wrap(err, "切换到上传视频失败")
	}

	time.Sleep(1 * time.Second)

	return &PublishAction{page: pp}, nil
}

// PublishVideo 上传视频并提交
func (p *PublishAction) PublishVideo(ctx context.Context, content PublishVideoContent) error {
	if content.VideoPath == "" {
		return errors.New("视频不能为空")
	}

	page := p.page.Context(ctx)

	if err := uploadVideo(page, content.VideoPath); err != nil {
		return errors.Wrap(err, "小红书上传视频失败")
	}

	if err := submitPublishVideo(page, content.Title, content.Content, content.Tags, content.Location, content.ScheduleTime); err != nil {
		return errors.Wrap(err, "小红书发布失败")
	}
	return nil
}

// uploadVideo 上传单个本地视频
func uploadVideo(page *rod.Page, videoPath string) error {
	pp := page.Timeout(5 * time.Minute) // 视频处理耗时更长

	if _, err := os.Stat(videoPath); os.IsNotExist(err) {
		return errors.Wrapf(err, "视频文件不存在: %s", videoPath)
	}

	// 寻找文件上传输入框（与图文一致的 class，或退回到 input[type=file]）
	var fileInput *rod.Element
	var err error
	fileInput, err = pp.Element(".upload-input")
	if err != nil || fileInput == nil {
		fileInput, err = pp.Element("input[type='file']")
		if err != nil || fileInput == nil {
			return errors.New("未找到视频上传输入框")
		}
	}

	fileInput.MustSetFiles(videoPath)

	// 对于视频，等待发布按钮变为可点击即表示处理完成
	btn, err := waitForPublishButtonClickable(pp)
	if err != nil {
		return err
	}
	slog.Info("视频上传/处理完成，发布按钮可点击", "btn", btn)
	return nil
}

// waitForPublishButtonClickable 等待发布按钮可点击
func waitForPublishButtonClickable(page *rod.Page) (*rod.Element, error) {
	maxWait := 10 * time.Minute
	interval := 1 * time.Second
	start := time.Now()
	selectors := []string{
		"#web > div > div > div.publish-page-container > div.publish-page-publish-btn > button.d-button.d-button-default.d-button-with-content.--color-static.bold.--color-bg-fill.--color-text-paragraph.custom-button.bg-red",
		"#web div.publish-page-container div.publish-page-publish-btn button.custom-button.bg-red",
		"#web div.publish-page-container div.publish-page-publish-btn button",
		"div.publish-page-publish-btn button",
		"button.publishBtn",
	}

	slog.Info("开始等待发布按钮可点击(视频)")

	for time.Since(start) < maxWait {
		for _, selector := range selectors {
			btn, err := page.Element(selector)
			if err != nil || btn == nil {
				continue
			}
			vis, verr := btn.Visible()
			if verr != nil || !vis {
				continue
			}
			if disabled, _ := btn.Attribute("disabled"); disabled != nil {
				continue
			}
			if cls, _ := btn.Attribute("class"); cls != nil && strings.Contains(*cls, "disabled") {
				continue
			}
			return btn, nil
		}
		time.Sleep(interval)
	}
	return nil, errors.New("等待发布按钮可点击超时")
}

// submitPublishVideo 填写标题、正文、标签并点击发布（等待按钮可点击后再提交）
func submitPublishVideo(page *rod.Page, title, content string, tags []string, location string, scheduleTime *time.Time) error {
	_ = location

	// 标题
	titleElem, err := page.Element("div.d-input input")
	if err != nil {
		return errors.Wrap(err, "查找标题输入框失败")
	}
	err = titleElem.Input(title)
	if err != nil {
		return errors.Wrap(err, "输入标题失败")
	}
	time.Sleep(1 * time.Second)

	// 正文 + 标签
	contentElem, ok := getContentElement(page)
	if !ok {
		return errors.New("没有找到内容输入框")
	}
	err = contentElem.Input(content)
	if err != nil {
		return errors.Wrap(err, "输入正文失败")
	}
	err = inputTags(contentElem, tags)
	if err != nil {
		return err
	}

	time.Sleep(1 * time.Second)

	// if err := setPublishLocation(page, location); err != nil {
	// 	return errors.Wrap(err, "设置地点失败")
	// }

	// 处理定时发布
	if scheduleTime != nil {
		err = setSchedulePublish(page, *scheduleTime)
		if err != nil {
			return errors.Wrap(err, "设置定时发布失败")
		}
		slog.Info("定时发布设置完成", "schedule_time", scheduleTime.Format("2006-01-02 15:04"))
	}

	// 等待发布按钮可点击
	btn, err := waitForPublishButtonClickable(page)
	if err != nil {
		return err
	}

	// 点击发布
	err = btn.Click(proto.InputMouseButtonLeft, 1)
	if err != nil {
		return errors.Wrap(err, "点击发布按钮失败")
	}

	time.Sleep(3 * time.Second)
	return nil
}
