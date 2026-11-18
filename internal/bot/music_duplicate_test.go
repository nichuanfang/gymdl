package bot

import "testing"

func TestDuplicateConfirmTextKeepsRoom(t *testing.T) {
	text := duplicateConfirmText("晴天", "周杰伦", "叶惠美", "同一个链接")
	want := "可能是同一首歌\n\n晴天\n周杰伦\n叶惠美\n\n同一个链接\n\n要再存一份吗？"
	if text != want {
		t.Fatalf("%q", text)
	}
	sparse := duplicateConfirmText("晴天", "未知", "", "歌名、歌手和专辑一致")
	if sparse != "可能是同一首歌\n\n晴天\n\n歌名、歌手和专辑一致\n\n要再存一份吗？" {
		t.Fatalf("%q", sparse)
	}
}
