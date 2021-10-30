package main

import (
	"time"

	_ "github.com/mattn/go-sqlite3"
	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
)

func HandleMessage(source mautrix.EventSource, event *mevent.Event) {
	if event.Sender.String() == App.configuration.Username {
		log.Infof("Event %s is from us, so not going to respond.", event.ID)
		return
	}

	now := time.Now()
	if now.Sub(App.mostRecentSend[event.RoomID]).Minutes() < App.configuration.VacationMessageMinInterval {
		log.Infof("Already sent a vacation message to %s in the past %f minutes.", event.RoomID, App.configuration.VacationMessageMinInterval)
		return
	}
	App.mostRecentSend[event.RoomID] = now

	content := format.RenderMarkdown(App.configuration.VacationMessage, true, true)
	SendMessage(event.RoomID, &content)
}
