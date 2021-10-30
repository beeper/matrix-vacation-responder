package main

import (
	"database/sql"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"

	"git.sr.ht/~sumner/matrix-vacation-responder/store"
)

type VacationResponder struct {
	client        *mautrix.Client
	configuration Configuration
	olmMachine    *mcrypto.OlmMachine
	stateStore    *store.StateStore

	// Most recent send to room.
	mostRecentSend map[mid.RoomID]time.Time
}

var App VacationResponder

var VERSION = "0.4.0"

func main() {
	// Arg parsing
	configPath := flag.String("config", "./config.yaml", "config file location")
	logLevelStr := flag.String("loglevel", "debug", "the log level")
	logFilename := flag.String("logfile", "", "the log file to use (defaults to '' meaning no log file)")
	dbFilename := flag.String("dbfile", "./vacation_responder.db", "the SQLite DB file to use")
	flag.Parse()

	// Configure logging
	if *logFilename != "" {
		logFile, err := os.OpenFile(*logFilename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			mw := io.MultiWriter(os.Stdout, logFile)
			log.SetOutput(mw)
		} else {
			log.Errorf("Failed to open logging file; using default stderr: %s", err)
		}
	}
	log.SetFormatter(&log.JSONFormatter{})
	log.SetLevel(log.DebugLevel)
	logLevel, err := log.ParseLevel(*logLevelStr)
	if err == nil {
		log.SetLevel(logLevel)
	} else {
		log.Errorf("Invalid loglevel '%s'. Using default 'debug'.", logLevel)
	}

	log.Info("vacation responder starting...")

	// Load configuration
	log.Infof("Reading config from %s...", *configPath)
	configYaml, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Could not read config from %s: %s", *configPath, err)
	}

	// Default configuration values
	App.configuration = Configuration{}
	App.mostRecentSend = make(map[mid.RoomID]time.Time)
	if err := App.configuration.Parse(configYaml); err != nil {
		log.Fatal("Failed to read config!")
	}
	username := mid.UserID(App.configuration.Username)
	_, _, err = username.Parse()
	if err != nil {
		log.Fatal("Couldn't parse username")
	}

	// Open the config database
	db, err := sql.Open("sqlite3", *dbFilename)
	if err != nil {
		log.Fatal("Could not open vacation responder database.")
	}

	// Make sure to exit cleanly
	c := make(chan os.Signal, 1)
	signal.Notify(c,
		os.Interrupt,
		os.Kill,
		syscall.SIGABRT,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)
	go func() {
		for range c { // when the process is killed
			log.Info("Cleaning up")
			db.Close()
			os.Exit(0)
		}
	}()

	App.stateStore = store.NewStateStore(db)
	if err := App.stateStore.CreateTables(); err != nil {
		log.Fatal("Failed to create the tables for vacation responder.", err)
	}

	log.Infof("Logging in %s", App.configuration.Username)
	password, err := App.configuration.GetPassword()
	if err != nil {
		log.Fatalf("Could not read password from %s", App.configuration.PasswordFile)
	}
	deviceID := FindDeviceID(db, username.String())
	if len(deviceID) > 0 {
		log.Info("Found existing device ID in database:", deviceID)
	}
	App.client, err = mautrix.NewClient(App.configuration.Homeserver, "", "")
	if err != nil {
		panic(err)
	}
	_, err = DoRetry("login", func() (interface{}, error) {
		return App.client.Login(&mautrix.ReqLogin{
			Type: mautrix.AuthTypePassword,
			Identifier: mautrix.UserIdentifier{
				Type: mautrix.IdentifierTypeUser,
				User: username.String(),
			},
			Password:                 password,
			InitialDeviceDisplayName: "vacation responder",
			DeviceID:                 deviceID,
			StoreCredentials:         true,
		})
	})
	if err != nil {
		log.Fatalf("Couldn't login to the homeserver.")
	}
	log.Infof("Logged in as %s/%s", App.client.UserID, App.client.DeviceID)

	// set the client store on the client.
	App.client.Store = App.stateStore

	// Setup the crypto store
	sqlCryptoStore := mcrypto.NewSQLCryptoStore(
		db,
		"sqlite3",
		username.String(),
		App.client.DeviceID,
		[]byte("standupbot_cryptostore_key"),
		CryptoLogger{},
	)
	err = sqlCryptoStore.CreateTables()
	if err != nil {
		log.Fatal("Could not create tables for the SQL crypto store.")
	}

	App.olmMachine = mcrypto.NewOlmMachine(App.client, &CryptoLogger{}, sqlCryptoStore, App.stateStore)
	err = App.olmMachine.Load()
	if err != nil {
		log.Errorf("Could not initialize encryption support. Encrypted rooms will not work.")
	}

	syncer := App.client.Syncer.(*mautrix.DefaultSyncer)
	// Hook up the OlmMachine into the Matrix client so it receives e2ee
	// keys and other such things.
	syncer.OnSync(func(resp *mautrix.RespSync, since string) bool {
		App.olmMachine.ProcessSyncResponse(resp, since)
		return true
	})

	syncer.OnEventType(mevent.StateMember, func(_ mautrix.EventSource, event *mevent.Event) {
		App.olmMachine.HandleMemberEvent(event)
		App.stateStore.SetMembership(event)

		if event.GetStateKey() == username.String() && event.Content.AsMember().Membership == mevent.MembershipInvite {
			log.Info("Joining ", event.RoomID)
			_, err := DoRetry("join room", func() (interface{}, error) {
				return App.client.JoinRoomByID(event.RoomID)
			})
			if err != nil {
				log.Errorf("Could not join channel %s. Error %+v", event.RoomID.String(), err)
			} else {
				log.Infof("Joined %s sucessfully", event.RoomID.String())
			}
		} else if event.GetStateKey() == username.String() && event.Content.AsMember().Membership.IsLeaveOrBan() {
			log.Infof("Left or banned from %s", event.RoomID)
		}
	})

	syncer.OnEventType(mevent.StateEncryption, func(_ mautrix.EventSource, event *mevent.Event) {
		App.stateStore.SetEncryptionEvent(event)
	})

	syncer.OnEventType(mevent.EventMessage, func(source mautrix.EventSource, event *mevent.Event) { go HandleMessage(source, event) })

	syncer.OnEventType(mevent.EventEncrypted, func(source mautrix.EventSource, event *mevent.Event) {
		decryptedEvent, err := App.olmMachine.DecryptMegolmEvent(event)
		if err != nil {
			log.Errorf("Failed to decrypt message from %s in %s: %+v", event.Sender, event.RoomID, err)
		} else {
			log.Debugf("Received encrypted event from %s in %s", event.Sender, event.RoomID)
			if decryptedEvent.Type == mevent.EventMessage {
				go HandleMessage(source, decryptedEvent)
			}
		}
	})

	for {
		log.Debugf("Running sync...")
		err = App.client.Sync()
		if err != nil {
			log.Errorf("Sync failed. %+v", err)
		}
	}
}

func FindDeviceID(db *sql.DB, accountID string) (deviceID mid.DeviceID) {
	err := db.QueryRow("SELECT device_id FROM crypto_account WHERE account_id=$1", accountID).Scan(&deviceID)
	if err != nil && err != sql.ErrNoRows {
		log.Warnf("Failed to scan device ID: %v", err)
	}
	return
}
