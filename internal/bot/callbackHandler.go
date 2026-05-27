package bot

import (
    "encoding/json"

    tb "gopkg.in/telebot.v4"
)

// CallbackData 结构化协议
type CallbackData struct {
	Action string `json:"a"`
	ID     string `json:"i"`
}

// HandleCallBack 统一入口
func (app *BotApp) HandleCallBack(c tb.Context) error {
	var data CallbackData
	if err := json.Unmarshal([]byte(c.Data()), &data); err != nil {
		return c.Respond(&tb.CallbackResponse{Text: "解析错误"})
	}

	// 根据 Action 分发
	switch data.Action {
	// case "deleteSong":
	// 	return handleDeleteSong(c, app, data)
	default:
		return c.Respond(&tb.CallbackResponse{Text: "未知指令"})
	}
}