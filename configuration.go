package main

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type Configuration struct {
	// Authentication settings
	Homeserver   string `yaml:"homeserver"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`

	VacationMessage string `yaml:"vacation_message"`
}

func (c *Configuration) Parse(data []byte) error {
	return yaml.Unmarshal(data, c)
}

func (c *Configuration) GetPassword() (string, error) {
	log.Debug("Reading password from ", c.PasswordFile)
	buf, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(buf)), nil
}
