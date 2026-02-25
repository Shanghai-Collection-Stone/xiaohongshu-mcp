package xiaohongshu

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// PublishImageContent 发布图文内容
type PublishImageContent struct {
	Title        string
	Content      string
	Tags         []string
	ImagePaths   []string
	Location     string
	ScheduleTime *time.Time // 定时发布时间，nil 表示立即发布
}

type PublishAction struct {
	page *rod.Page
}

const (
	urlOfPublic = `https://creator.xiaohongshu.com/publish/publish?source=official`
)

func NewPublishImageAction(page *rod.Page) (*PublishAction, error) {

	pp := page.Timeout(300 * time.Second)

	// 使用更稳健的导航和等待策略
	if err := pp.Navigate(urlOfPublic); err != nil {
		return nil, errors.Wrap(err, "导航到发布页面失败")
	}

	// 等待页面加载，使用 WaitLoad 代替 WaitIdle（更宽松）
	if err := pp.WaitLoad(); err != nil {
		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
	}
	time.Sleep(2 * time.Second)

	// 等待页面稳定
	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
	}
	time.Sleep(1 * time.Second)

	if err := mustClickPublishTab(pp, "上传图文"); err != nil {
		logrus.Errorf("点击上传图文 TAB 失败: %v", err)
		return nil, err
	}

	time.Sleep(1 * time.Second)

	return &PublishAction{
		page: pp,
	}, nil
}

func (p *PublishAction) Publish(ctx context.Context, content PublishImageContent) error {
	if len(content.ImagePaths) == 0 {
		return errors.New("图片不能为空")
	}

	page := p.page.Context(ctx)

	uploadedCount, err := uploadImages(page, content.ImagePaths)
	if err != nil {
		return errors.Wrap(err, "小红书上传图片失败")
	}
	if err := waitForUploadComplete(page, uploadedCount); err != nil {
		return errors.Wrap(err, "小红书上传图片未完成")
	}

	tags := content.Tags
	if len(tags) >= 10 {
		logrus.Warnf("标签数量超过10，截取前10个标签")
		tags = tags[:10]
	}

	logrus.Infof("发布内容: title=%s, images=%v, tags=%v, location=%s, schedule=%v", content.Title, len(content.ImagePaths), tags, content.Location, content.ScheduleTime)

	if err := submitPublish(page, content.Title, content.Content, tags, content.Location, content.ScheduleTime); err != nil {
		return errors.Wrap(err, "小红书发布失败")
	}

	return nil
}

func removePopCover(page *rod.Page) {

	// 先移除弹窗封面
	has, elem, err := page.Has("div.d-popover")
	if err != nil {
		return
	}
	if has {
		elem.MustRemove()
	}

	// 兜底：点击一下空位置吧
	clickEmptyPosition(page)
}

func clickEmptyPosition(page *rod.Page) {
	x := 380 + rand.Intn(100)
	y := 20 + rand.Intn(60)
	page.Mouse.MustMoveTo(float64(x), float64(y)).MustClick(proto.InputMouseButtonLeft)
}

func mustClickPublishTab(page *rod.Page, tabname string) error {
	page.MustElement(`div.upload-content`).MustWaitVisible()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tab, blocked, err := getTabElement(page, tabname)
		if err != nil {
			logrus.Warnf("获取发布 TAB 元素失败: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if tab == nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if blocked {
			logrus.Info("发布 TAB 被遮挡，尝试移除遮挡")
			removePopCover(page)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if err := tab.Click(proto.InputMouseButtonLeft, 1); err != nil {
			logrus.Warnf("点击发布 TAB 失败: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		return nil
	}

	return errors.Errorf("没有找到发布 TAB - %s", tabname)
}

func getTabElement(page *rod.Page, tabname string) (*rod.Element, bool, error) {
	elems, err := page.Elements("div.creator-tab")
	if err != nil {
		return nil, false, err
	}

	for _, elem := range elems {
		if !isElementVisible(elem) {
			continue
		}

		text, err := elem.Text()
		if err != nil {
			logrus.Debugf("获取发布 TAB 文本失败: %v", err)
			continue
		}

		if strings.TrimSpace(text) != tabname {
			continue
		}

		blocked, err := isElementBlocked(elem)
		if err != nil {
			return nil, false, err
		}

		return elem, blocked, nil
	}

	return nil, false, nil
}

func isElementBlocked(elem *rod.Element) (bool, error) {
	result, err := elem.Eval(`() => {
		const rect = this.getBoundingClientRect();
		if (rect.width === 0 || rect.height === 0) {
			return true;
		}
		const x = rect.left + rect.width / 2;
		const y = rect.top + rect.height / 2;
		const target = document.elementFromPoint(x, y);
		return !(target === this || this.contains(target));
	}`)
	if err != nil {
		return false, err
	}

	return result.Value.Bool(), nil
}

func uploadImages(page *rod.Page, imagesPaths []string) (int, error) {
	// 验证文件路径有效性
	validPaths := make([]string, 0, len(imagesPaths))
	for _, path := range imagesPaths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			logrus.Warnf("图片文件不存在: %s", path)
			continue
		}
		validPaths = append(validPaths, path)
		logrus.Infof("获取有效图片：%s", path)
	}
	if len(validPaths) == 0 {
		return 0, errors.New("没有可用的本地图片文件")
	}

	// 逐张上传：每张上传后等待预览出现，再上传下一张
	for i, path := range validPaths {
		selector := `input[type="file"]`
		if i == 0 {
			selector = ".upload-input"
		}

		uploadInput, err := page.Element(selector)
		if err != nil {
			return 0, errors.Wrapf(err, "查找上传输入框失败(第%d张)", i+1)
		}
		if err := uploadInput.SetFiles([]string{path}); err != nil {
			return 0, errors.Wrapf(err, "上传第%d张图片失败", i+1)
		}

		slog.Info("图片已提交上传", "index", i+1, "path", path)

		// 等待当前图片上传完成（预览元素数量达到 i+1），并且上传状态稳定
		if err := waitForUploadComplete(page, i+1); err != nil {
			return 0, errors.Wrapf(err, "第%d张图片上传超时", i+1)
		}
		time.Sleep(1 * time.Second)
	}

	return len(validPaths), nil
}

func waitForUploadComplete(page *rod.Page, expectedCount int) error {
	maxWaitTime := 3 * time.Minute
	checkInterval := 600 * time.Millisecond
	start := time.Now()
	stableSince := time.Time{}
	lastLog := time.Time{}

	for time.Since(start) < maxWaitTime {
		st, err := probeImageUploadState(page)
		if err != nil {
			time.Sleep(checkInterval)
			continue
		}
		if st.ErrorCount > 0 {
			return errors.Errorf("检测到图片上传失败提示: error_count=%d", st.ErrorCount)
		}

		ready := st.PreviewCount >= expectedCount && st.ReadyCount >= expectedCount && st.UploadingCount == 0
		if ready {
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= 2*time.Second {
				slog.Info("图片上传完成", "count", st.PreviewCount)
				return nil
			}
		} else {
			stableSince = time.Time{}
		}

		if lastLog.IsZero() || time.Since(lastLog) >= 3*time.Second {
			slog.Info("等待图片上传", "preview", st.PreviewCount, "ready", st.ReadyCount, "uploading", st.UploadingCount, "expected", expectedCount)
			lastLog = time.Now()
		}

		time.Sleep(checkInterval)
	}

	return errors.Errorf("图片上传超时(%s)，请检查网络连接和图片大小", maxWaitTime)
}

type imageUploadState struct {
	PreviewCount   int `json:"previewCount"`
	ReadyCount     int `json:"readyCount"`
	UploadingCount int `json:"uploadingCount"`
	ErrorCount     int `json:"errorCount"`
}

func probeImageUploadState(page *rod.Page) (imageUploadState, error) {
	res, err := page.Eval(`() => {
		const root = document.querySelector(".img-preview-area") || document;
		const previewSelectors = [
			".img-preview-area .pr",
			".img-preview-area .img-preview",
			".img-preview-area [class*='preview']",
		];
		const previews = [];
		for (const sel of previewSelectors) {
			for (const n of Array.from(document.querySelectorAll(sel))) {
				if (!previews.includes(n)) previews.push(n);
			}
		}

		const textContains = (node, re) => {
			const t = (node && node.textContent) ? node.textContent : "";
			return re.test(t);
		};

		const isVisible = (el) => {
			if (!el) return false;
			const rect = el.getBoundingClientRect();
			return rect.width > 0 && rect.height > 0;
		};

		let uploadingCount = 0;
		let errorCount = 0;
		let readyCount = 0;
		for (const p of previews) {
			const t = (p.textContent || "").trim();
			if (/上传失败|失败|重试|重新上传/.test(t)) {
				errorCount++;
				continue;
			}
			if (/上传中|处理中|排队中|等待中/.test(t)) {
				uploadingCount++;
				continue;
			}

			const progressLike = p.querySelector("[class*='progress'],[class*='loading'],.d-loading,.loading,.progress,.spin,.spinner");
			if (progressLike && isVisible(progressLike)) {
				uploadingCount++;
				continue;
			}

			const img = p.querySelector("img");
			if (img && img.complete && img.naturalWidth > 0) {
				readyCount++;
				continue;
			}

			const bg = window.getComputedStyle(p).backgroundImage;
			if (bg && bg !== "none") {
				readyCount++;
				continue;
			}
		}

		const pageText = (document.body && document.body.innerText) ? document.body.innerText : "";
		if (/上传失败|图片上传失败|上传出错/.test(pageText)) {
			errorCount = Math.max(errorCount, 1);
		}

		return JSON.stringify({
			previewCount: previews.length,
			readyCount,
			uploadingCount,
			errorCount,
		});
	}`)
	if err != nil {
		return imageUploadState{}, err
	}

	raw := res.Value.String()
	var st imageUploadState
	if uerr := json.Unmarshal([]byte(raw), &st); uerr != nil {
		return imageUploadState{}, uerr
	}
	return st, nil
}

type publishCheckState struct {
	ToastText string `json:"toastText"`
}

func waitForPublishResult(page *rod.Page) error {
	maxWaitTime := 90 * time.Second
	checkInterval := 700 * time.Millisecond
	start := time.Now()
	initialURL := ""
	if info, err := page.Info(); err == nil && info != nil {
		initialURL = info.URL
	}

	lastLog := time.Time{}
	for time.Since(start) < maxWaitTime {
		curURL := ""
		if info, err := page.Info(); err == nil && info != nil {
			curURL = info.URL
		}
		if initialURL != "" && curURL != "" && curURL != initialURL && !strings.Contains(curURL, "/publish/publish") {
			slog.Info("发布页面已跳转", "from", initialURL, "to", curURL)
			return nil
		}

		st, err := probePublishState(page)
		if err == nil && strings.TrimSpace(st.ToastText) != "" {
			switch classifyPublishMessage(st.ToastText) {
			case publishMessageSuccess:
				slog.Info("检测到发布成功提示", "toast", shortenForLog(st.ToastText, 200))
				return nil
			case publishMessageError:
				return errors.Errorf("检测到发布失败提示: %s", shortenForLog(st.ToastText, 240))
			}
		}

		if lastLog.IsZero() || time.Since(lastLog) >= 5*time.Second {
			slog.Info("等待发布结果", "url", shortenForLog(curURL, 180))
			lastLog = time.Now()
		}
		time.Sleep(checkInterval)
	}

	return errors.Errorf("等待发布结果超时(%s)", maxWaitTime)
}

func shortenForLog(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	return string(rs[:maxRunes]) + "…"
}

type publishMessageKind int

const (
	publishMessageUnknown publishMessageKind = iota
	publishMessageSuccess
	publishMessageError
)

func classifyPublishMessage(msg string) publishMessageKind {
	m := strings.TrimSpace(msg)
	if m == "" {
		return publishMessageUnknown
	}
	if strings.Contains(m, "发布成功") || strings.Contains(m, "已发布") || strings.Contains(m, "发布完成") || strings.Contains(m, "提交成功") || strings.Contains(m, "审核中") || strings.Contains(m, "发布中") {
		return publishMessageSuccess
	}
	if strings.Contains(m, "发布失败") || strings.Contains(m, "失败") || strings.Contains(m, "出错") || strings.Contains(m, "错误") {
		return publishMessageError
	}
	return publishMessageUnknown
}

func probePublishState(page *rod.Page) (publishCheckState, error) {
	res, err := page.Eval(`() => {
		const isVisible = (el) => {
			if (!el) return false;
			const rect = el.getBoundingClientRect();
			return rect.width > 0 && rect.height > 0;
		};

		const candidates = [];
		const selectors = [
			".d-message",
			".d-toast",
			".toast",
			"[role='alert']",
			".message",
			".d-notification",
		];
		for (const sel of selectors) {
			for (const el of Array.from(document.querySelectorAll(sel))) {
				if (isVisible(el)) candidates.push(el);
			}
		}
		let toastText = "";
		if (candidates.length > 0) {
			const last = candidates[candidates.length - 1];
			toastText = (last.textContent || "").trim();
		}
		if (!toastText) {
			const bodyText = (document.body && document.body.innerText) ? document.body.innerText : "";
			const m = bodyText.match(/发布成功|提交成功|审核中|发布失败|上传失败|失败/);
			if (m && m[0]) toastText = m[0];
		}
		return JSON.stringify({ toastText });
	}`)
	if err != nil {
		return publishCheckState{}, err
	}
	raw := res.Value.String()
	var st publishCheckState
	if uerr := json.Unmarshal([]byte(raw), &st); uerr != nil {
		return publishCheckState{}, uerr
	}
	return st, nil
}

func submitPublish(page *rod.Page, title, content string, tags []string, location string, scheduleTime *time.Time) error {
	_ = location

	titleElem, err := page.Element("div.d-input input")
	if err != nil {
		return errors.Wrap(err, "查找标题输入框失败")
	}
	err = titleElem.Input(title)
	if err != nil {
		return errors.Wrap(err, "输入标题失败")
	}

	// 检查标题长度
	time.Sleep(500 * time.Millisecond)
	err = checkTitleMaxLength(page)
	if err != nil {
		return err
	}
	slog.Info("检查标题长度：通过")

	time.Sleep(1 * time.Second)

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

	// 检查正文长度
	err = checkContentMaxLength(page)
	if err != nil {
		return err
	}
	slog.Info("检查正文长度：通过")

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

	submitButton, err := waitForPublishButtonClickable(page)
	if err != nil {
		return err
	}
	err = submitButton.Click(proto.InputMouseButtonLeft, 1)
	if err != nil {
		return errors.Wrap(err, "点击发布按钮失败")
	}

	return waitForPublishResult(page)
}

func findPublishButton(page *rod.Page) (*rod.Element, error) {
	pp := page.Timeout(30 * time.Second)
	selectors := []string{
		"#web > div > div > div.publish-page-container > div.publish-page-publish-btn > button.d-button.d-button-default.d-button-with-content.--color-static.bold.--color-bg-fill.--color-text-paragraph.custom-button.bg-red",
		"#web div.publish-page-container div.publish-page-publish-btn button.custom-button.bg-red",
		"#web div.publish-page-container div.publish-page-publish-btn button",
		"div.publish-page-publish-btn button",
		"button.publishBtn",
		"div.submit div.d-button-content",
	}
	for _, sel := range selectors {
		el, err := pp.Element(sel)
		if err != nil || el == nil {
			continue
		}
		vis, verr := el.Visible()
		if verr == nil && vis {
			return el, nil
		}
	}
	return nil, errors.New("未找到发布按钮")
}

func setPublishLocation(page *rod.Page, location string) error {
	location = strings.TrimSpace(location)
	if location == "" {
		return nil
	}

	pp := page.Timeout(20 * time.Second)

	if err := openLocationPanel(pp); err != nil {
		return err
	}
	if err := searchAndSelectLocation(pp, location); err != nil {
		return err
	}

	return nil
}

var _ = setPublishLocation

func openLocationPanel(page *rod.Page) error {
	inputs, _ := page.Elements("input[placeholder*='地点']")
	for _, in := range inputs {
		if !isElementVisible(in) {
			continue
		}
		if err := in.Click(proto.InputMouseButtonLeft, 1); err == nil {
			time.Sleep(300 * time.Millisecond)
			return nil
		}
	}

	buttons, _ := page.Elements("button")
	for _, btn := range buttons {
		if !isElementVisible(btn) {
			continue
		}
		t, err := btn.Text()
		if err != nil {
			continue
		}
		t = strings.TrimSpace(t)
		if t == "添加地点" || t == "地点" || t == "添加位置" {
			if err := btn.Click(proto.InputMouseButtonLeft, 1); err == nil {
				time.Sleep(300 * time.Millisecond)
				return nil
			}
		}
	}

	spans, _ := page.Elements("span")
	for _, sp := range spans {
		if !isElementVisible(sp) {
			continue
		}
		t, err := sp.Text()
		if err != nil {
			continue
		}
		t = strings.TrimSpace(t)
		if t == "添加地点" || t == "地点" || t == "添加位置" {
			if err := sp.Click(proto.InputMouseButtonLeft, 1); err == nil {
				time.Sleep(300 * time.Millisecond)
				return nil
			}
		}
	}

	return errors.New("未找到地点入口")
}

func searchAndSelectLocation(page *rod.Page, location string) error {
	var inputElem *rod.Element

	if e, err := page.Element("input[placeholder*='搜索'][placeholder*='地点']"); err == nil && e != nil {
		inputElem = e
	} else if e, err := page.Element("input[placeholder*='搜索']"); err == nil && e != nil {
		inputElem = e
	} else {
		inputs, _ := page.Elements("input")
		for _, in := range inputs {
			if !isElementVisible(in) {
				continue
			}
			if ph, _ := in.Attribute("placeholder"); ph != nil && strings.Contains(*ph, "搜索") {
				inputElem = in
				break
			}
		}
	}

	if inputElem == nil {
		return errors.New("未找到地点搜索输入框")
	}

	_ = inputElem.SelectAllText()
	inputElem.MustInput(location)
	inputElem.MustKeyActions().Press(input.Enter).MustDo()
	tryWaitLocationSuggestions(page)

	selectors := []string{
		"li.el-select-dropdown__item",
		"div.el-select-dropdown__item",
		"div.d-select-option",
		"div.item",
		"div.poi-item",
		"div.search-item",
	}

	var items []*rod.Element
	for _, sel := range selectors {
		es, err := page.Elements(sel)
		if err != nil || len(es) == 0 {
			continue
		}
		items = es
		break
	}

	if len(items) == 0 {
		return errors.New("未找到地点候选列表")
	}

	var fallback *rod.Element
	for _, it := range items {
		if !isElementVisible(it) {
			continue
		}
		if fallback == nil {
			fallback = it
		}
		t, err := it.Text()
		if err != nil {
			continue
		}
		if strings.Contains(strings.TrimSpace(t), location) {
			if err := it.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return errors.Wrap(err, "点击匹配地点失败")
			}
			time.Sleep(400 * time.Millisecond)
			return nil
		}
	}

	if fallback == nil {
		return errors.New("地点候选列表为空")
	}
	if err := fallback.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击地点候选失败")
	}
	time.Sleep(400 * time.Millisecond)
	return nil
}

func tryWaitLocationSuggestions(page *rod.Page) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := page.Eval(`() => {
			const items = document.querySelectorAll('li.el-select-dropdown__item, div.el-select-dropdown__item, div.d-select-option, div.poi-item, div.search-item, div.item');
			return items && items.length;
		}`)
		if err == nil && r.Value.Num() > 0 {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// 检查标题是否超过最大长度
func checkTitleMaxLength(page *rod.Page) error {
	has, elem, err := page.Has(`div.title-container div.max_suffix`)
	if err != nil {
		return errors.Wrap(err, "检查标题长度元素失败")
	}

	// 元素不存在，说明标题没超长
	if !has {
		return nil
	}

	// 元素存在，说明标题超长
	titleLength, err := elem.Text()
	if err != nil {
		return errors.Wrap(err, "获取标题长度文本失败")
	}

	return makeMaxLengthError(titleLength)
}

func checkContentMaxLength(page *rod.Page) error {
	has, elem, err := page.Has(`div.edit-container div.length-error`)
	if err != nil {
		return errors.Wrap(err, "检查正文长度元素失败")
	}

	// 元素不存在，说明正文没超长
	if !has {
		return nil
	}

	// 元素存在，说明正文超长
	contentLength, err := elem.Text()
	if err != nil {
		return errors.Wrap(err, "获取正文长度文本失败")
	}

	return makeMaxLengthError(contentLength)
}

func makeMaxLengthError(elemText string) error {
	parts := strings.Split(elemText, "/")
	if len(parts) != 2 {
		return errors.Errorf("长度超过限制: %s", elemText)
	}

	currLen, maxLen := parts[0], parts[1]

	return errors.Errorf("当前输入长度为%s，最大长度为%s", currLen, maxLen)
}

// 查找内容输入框 - 使用Race方法处理两种样式
func getContentElement(page *rod.Page) (*rod.Element, bool) {
	var foundElement *rod.Element
	var found bool

	page.Race().
		Element("div.ql-editor").MustHandle(func(e *rod.Element) {
		foundElement = e
		found = true
	}).
		ElementFunc(func(page *rod.Page) (*rod.Element, error) {
			return findTextboxByPlaceholder(page)
		}).MustHandle(func(e *rod.Element) {
		foundElement = e
		found = true
	}).
		MustDo()

	if found {
		return foundElement, true
	}

	slog.Warn("no content element found by any method")
	return nil, false
}

func inputTags(contentElem *rod.Element, tags []string) error {
	if len(tags) == 0 {
		return nil
	}

	time.Sleep(1 * time.Second)

	for range 20 {
		ka, err := contentElem.KeyActions()
		if err != nil {
			return errors.Wrap(err, "创建键盘操作失败")
		}
		if err := ka.Type(input.ArrowDown).Do(); err != nil {
			return errors.Wrap(err, "按下方向键失败")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ka, err := contentElem.KeyActions()
	if err != nil {
		return errors.Wrap(err, "创建键盘操作失败")
	}
	if err := ka.Press(input.Enter).Press(input.Enter).Do(); err != nil {
		return errors.Wrap(err, "按下回车键失败")
	}

	time.Sleep(1 * time.Second)

	for _, tag := range tags {
		tag = strings.TrimLeft(tag, "#")
		if err := inputTag(contentElem, tag); err != nil {
			return errors.Wrapf(err, "输入标签[%s]失败", tag)
		}
	}
	return nil
}

func inputTag(contentElem *rod.Element, tag string) error {
	if err := contentElem.Input("#"); err != nil {
		return errors.Wrap(err, "输入#失败")
	}
	time.Sleep(200 * time.Millisecond)

	for _, char := range tag {
		if err := contentElem.Input(string(char)); err != nil {
			return errors.Wrapf(err, "输入字符[%c]失败", char)
		}
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(1 * time.Second)

	page := contentElem.Page()
	topicContainer, err := page.Element("#creator-editor-topic-container")
	if err != nil || topicContainer == nil {
		slog.Warn("未找到标签联想下拉框，直接输入空格", "tag", tag)
		return contentElem.Input(" ")
	}

	firstItem, err := topicContainer.Element(".item")
	if err != nil || firstItem == nil {
		slog.Warn("未找到标签联想选项，直接输入空格", "tag", tag)
		return contentElem.Input(" ")
	}

	if err := firstItem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击标签联想选项失败")
	}
	slog.Info("成功点击标签联想选项", "tag", tag)
	time.Sleep(200 * time.Millisecond)

	time.Sleep(500 * time.Millisecond) // 等待标签处理完成
	return nil
}

func findTextboxByPlaceholder(page *rod.Page) (*rod.Element, error) {
	elements := page.MustElements("p")
	if elements == nil {
		return nil, errors.New("no p elements found")
	}

	// 查找包含指定placeholder的元素
	placeholderElem := findPlaceholderElement(elements, "输入正文描述")
	if placeholderElem == nil {
		return nil, errors.New("no placeholder element found")
	}

	// 向上查找textbox父元素
	textboxElem := findTextboxParent(placeholderElem)
	if textboxElem == nil {
		return nil, errors.New("no textbox parent found")
	}

	return textboxElem, nil
}

func findPlaceholderElement(elements []*rod.Element, searchText string) *rod.Element {
	for _, elem := range elements {
		placeholder, err := elem.Attribute("data-placeholder")
		if err != nil || placeholder == nil {
			continue
		}

		if strings.Contains(*placeholder, searchText) {
			return elem
		}
	}
	return nil
}

func findTextboxParent(elem *rod.Element) *rod.Element {
	currentElem := elem
	for range 5 {
		parent, err := currentElem.Parent()
		if err != nil {
			break
		}

		role, err := parent.Attribute("role")
		if err != nil || role == nil {
			currentElem = parent
			continue
		}

		if *role == "textbox" {
			return parent
		}

		currentElem = parent
	}
	return nil
}

// isElementVisible 检查元素是否可见
func isElementVisible(elem *rod.Element) bool {

	// 检查是否有隐藏样式
	style, err := elem.Attribute("style")
	if err == nil && style != nil {
		styleStr := *style

		if strings.Contains(styleStr, "left: -9999px") ||
			strings.Contains(styleStr, "top: -9999px") ||
			strings.Contains(styleStr, "position: absolute; left: -9999px") ||
			strings.Contains(styleStr, "display: none") ||
			strings.Contains(styleStr, "visibility: hidden") {
			return false
		}
	}

	visible, err := elem.Visible()
	if err != nil {
		slog.Warn("无法获取元素可见性", "error", err)
		return true
	}

	return visible
}

// setSchedulePublish 设置定时发布时间
func setSchedulePublish(page *rod.Page, t time.Time) error {
	slog.Info("开始设置定时发布", "schedule_time", t.Format(time.RFC3339))

	// 1. 点击定时发布开关
	if err := clickScheduleSwitch(page); err != nil {
		return err
	}
	time.Sleep(800 * time.Millisecond)

	// 2. 设置日期时间
	if err := setDateTime(page, t); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)

	if err := verifyScheduleDateTime(page, t); err != nil {
		return err
	}

	return nil
}

func clickScheduleSwitch(page *rod.Page) error {
	switchElem, err := page.Element(".post-time-wrapper .d-switch")
	if err != nil {
		return errors.Wrap(err, "查找定时发布开关失败")
	}

	enabled, err := isScheduleSwitchEnabled(switchElem)
	if err == nil && enabled {
		slog.Info("定时发布开关已开启，跳过点击")
		return nil
	}

	if err := switchElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击定时发布开关失败")
	}

	time.Sleep(300 * time.Millisecond)
	enabled, err = isScheduleSwitchEnabled(switchElem)
	if err != nil {
		slog.Info("已点击定时发布开关")
		return nil
	}
	if !enabled {
		return errors.New("定时发布开关点击后未生效")
	}
	slog.Info("定时发布开关已开启")
	return nil
}

func setDateTime(page *rod.Page, t time.Time) error {
	dateTimeStr := t.Format("2006-01-02 15:04")

	inputElem, err := page.Element(".date-picker-container input")
	if err != nil {
		return errors.Wrap(err, "查找日期时间输入框失败")
	}

	if err := inputElem.SelectAllText(); err != nil {
		return errors.Wrap(err, "选择日期时间文本失败")
	}
	if err := inputElem.Input(dateTimeStr); err != nil {
		return errors.Wrap(err, "输入日期时间失败")
	}
	inputElem.MustKeyActions().Press(input.Enter).MustDo()
	time.Sleep(200 * time.Millisecond)
	clickEmptyPosition(page)
	slog.Info("已设置日期时间", "datetime", dateTimeStr)

	return nil
}

func verifyScheduleDateTime(page *rod.Page, t time.Time) error {
	expected := t.Format("2006-01-02 15:04")
	inputEl, err := page.Element(".date-picker-container input")
	if err != nil {
		return errors.Wrap(err, "查找日期时间输入框失败")
	}
	val, err := inputEl.Attribute("value")
	if err != nil {
		return errors.Wrap(err, "读取日期时间输入框失败")
	}
	actual := ""
	if val != nil {
		actual = strings.TrimSpace(*val)
	}
	if actual == "" {
		r, err := inputEl.Property("value")
		if err == nil {
			actual = strings.TrimSpace(r.String())
		}
	}
	slog.Info("定时发布输入框回读", "expected", expected, "actual", actual)
	if actual == "" || !strings.Contains(actual, expected) {
		return errors.Errorf("定时发布时间未正确写入输入框: expected=%s actual=%s", expected, actual)
	}
	return nil
}

func isScheduleSwitchEnabled(elem *rod.Element) (bool, error) {
	if elem == nil {
		return false, errors.New("empty switch element")
	}
	if v, err := elem.Attribute("aria-checked"); err == nil && v != nil {
		return strings.EqualFold(strings.TrimSpace(*v), "true"), nil
	}
	if v, err := elem.Attribute("aria-pressed"); err == nil && v != nil {
		return strings.EqualFold(strings.TrimSpace(*v), "true"), nil
	}
	if cls, err := elem.Attribute("class"); err == nil && cls != nil {
		c := strings.ToLower(*cls)
		if strings.Contains(c, "checked") || strings.Contains(c, "on") || strings.Contains(c, "active") {
			return true, nil
		}
		if strings.Contains(c, "unchecked") || strings.Contains(c, "off") || strings.Contains(c, "inactive") {
			return false, nil
		}
	}
	return false, nil
}
