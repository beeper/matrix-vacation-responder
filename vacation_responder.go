package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	mid "maunium.net/go/mautrix/id"

	"git.sr.ht/~sumner/matrix-vacation-responder/store"
)

var client *mautrix.Client
var configuration Configuration
var olmMachine *mcrypto.OlmMachine
var stateStore *store.StateStore

var VERSION = "0.4.0"

func main() {
	// Arg parsing
	configPath := flag.String("config", "./config.json", "config file location")
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
	configJson, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Could not read config from %s: %s", *configPath, err)
	}

	// Default configuration values
	configuration = Configuration{}

	err = json.Unmarshal(configJson, &configuration)
	username := mid.UserID(configuration.Username)
	_, botHomeserver, err = username.Parse()
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

	stateStore = store.NewStateStore(db)
	if err := stateStore.CreateTables(); err != nil {
		log.Fatal("Failed to create the tables for vacation responder.", err)
	}

	log.Info("Logging in")
	password, err := configuration.GetPassword()
	if err != nil {
		log.Fatalf("Could not read password from %s", configuration.PasswordFile)
	}
	deviceID := FindDeviceID(db, username.String())
	if len(deviceID) > 0 {
		log.Info("Found existing device ID in database:", deviceID)
	}
	client, err = mautrix.NewClient(configuration.Homeserver, "", "")
	if err != nil {
		panic(err)
	}
	_, err = DoRetry("login", func() (interface{}, error) {
		return client.Login(&mautrix.ReqLogin{
			Type: mautrix.AuthTypePassword,
			Identifier: mautrix.UserIdentifier{
				Type: mautrix.IdentifierTypeUser,
				User: username.String(),
			},
			Password:                 password,
			InitialDeviceDisplayName: "chatwoot",
			DeviceID:                 deviceID,
			StoreCredentials:         true,
		})
	})
	if err != nil {
		log.Fatalf("Couldn't login to the homeserver.")
	}
	log.Infof("Logged in as %s/%s", client.UserID, client.DeviceID)
}

func FindDeviceID(db *sql.DB, accountID string) (deviceID mid.DeviceID) {
	err := db.QueryRow("SELECT device_id FROM crypto_account WHERE account_id=$1", accountID).Scan(&deviceID)
	if err != nil && err != sql.ErrNoRows {
		log.Warnf("Failed to scan device ID: %v", err)
	}
	return
}
