package bot

import (
	tb "gopkg.in/telebot.v4"
)

func (app *BotApp) registerHandlers() {
	app.bot.Use(app.LoggingMiddleware)
	app.bot.Use(app.AuthMiddleware)
    
	// 欢迎语
	app.bot.Handle("/start", StartCommand)

	// 帮助信息
	app.bot.Handle("/help", HelpCommand)
    
    // qq音乐登录
    app.bot.Handle("/qq_login",app.QQLoginCommand)

    // 歌单整理
    app.bot.Handle("/assign_playlist",app.AssignPlaylistCommand)
    
	// 指令注册器
	app.bot.Handle("/setCommands", SetCommands)

	// 普通文本
	app.bot.Handle(tb.OnText, HandleText)
    
    // 处理音频
    app.bot.Handle(tb.OnAudio,HandleAudio)

}