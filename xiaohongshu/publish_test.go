package xiaohongshu

import (
	"context"
	"testing"

	"github.com/xpzouying/xiaohongshu-mcp/browser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublish(t *testing.T) {

	t.Skip("SKIP: 测试发布")

	b, err := browser.NewBrowser(false)
	if err != nil {
		t.Fatalf("failed to launch browser: %v", err)
	}
	defer b.Close()

	page := b.NewPage()
	defer page.Close()

	action, err := NewPublishImageAction(page)
	require.NoError(t, err)

	err = action.Publish(context.Background(), PublishImageContent{
		Title:      "Hello World",
		Content:    "Hello World",
		ImagePaths: []string{"/tmp/1.jpg"},
	})
	assert.NoError(t, err)
}

func TestClassifyPublishMessage(t *testing.T) {
	cases := []struct {
		msg  string
		want publishMessageKind
	}{
		{msg: "", want: publishMessageUnknown},
		{msg: "发布成功", want: publishMessageSuccess},
		{msg: "提交成功，进入审核中", want: publishMessageSuccess},
		{msg: "审核中", want: publishMessageSuccess},
		{msg: "发布失败", want: publishMessageError},
		{msg: "上传失败，请重试", want: publishMessageError},
		{msg: "网络异常", want: publishMessageUnknown},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, classifyPublishMessage(c.msg), c.msg)
	}
}
