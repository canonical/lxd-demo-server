package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/lxc/lxd"
	"gopkg.in/fsnotify.v0"
	"gopkg.in/yaml.v2"
)

// Global variables
var lxdDaemon *lxd.Client
var config serverConfig

type serverConfig struct {
	Container string `yaml:"container"`
	Image     string `yaml:"image"`

	Feedback        bool `yaml:"feedback"`
	FeedbackTimeout int  `yaml:"feedback_timeout"`

	QuotaCPU       int `yaml:"quota_cpu"`
	QuotaDisk      int `yaml:"quota_disk"`
	QuotaProcesses int `yaml:"quota_processes"`
	QuotaRAM       int `yaml:"quota_ram"`
	QuotaSessions  int `yaml:"quota_sessions"`
	QuotaTime      int `yaml:"quota_time"`

	ServerAddr          string   `yaml:"server_addr"`
	ServerBannedIPs     []string `yaml:"server_banned_ips"`
	ServerConsoleOnly   bool     `yaml:"server_console_only"`
	ServerContainersMax int      `yaml:"server_containers_max"`
	ServerCPUCount      int      `yaml:"server_cpu_count"`
	ServerIPv6Only      bool     `yaml:"server_ipv6_only"`
	ServerMaintenance   bool     `yaml:"server_maintenance"`
	ServerTerms         string   `yaml:"server_terms"`

	serverTermsHash string
}

type statusCode int

const (
	serverOperational statusCode = 0
	serverMaintenance statusCode = 1

	containerStarted      statusCode = 0
	containerInvalidTerms statusCode = 1
	containerServerFull   statusCode = 2
	containerQuotaReached statusCode = 3
	containerUserBanned   statusCode = 4
	containerUnknownError statusCode = 5
)

func main() {
	rand.Seed(time.Now().UTC().UnixNano())
	err := run()
	if err != nil {
		fmt.Printf("error: %s\n", err)
		os.Exit(1)
	}
}

func parseConfig() error {
	data, err := ioutil.ReadFile("lxd-demo.yaml")
	if os.IsNotExist(err) {
		return fmt.Errorf("The configuration file (lxd-demo.yaml) doesn't exist.")
	} else if err != nil {
		return fmt.Errorf("Unable to read the configuration: %s", err)
	}

	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return fmt.Errorf("Unable to parse the configuration: %s", err)
	}

	if config.ServerAddr == "" {
		config.ServerAddr = ":8080"
	}

	config.ServerTerms = strings.TrimRight(config.ServerTerms, "\n")
	hash := sha256.New()
	io.WriteString(hash, config.ServerTerms)
	config.serverTermsHash = fmt.Sprintf("%x", hash.Sum(nil))

	if config.Container == "" && config.Image == "" {
		return fmt.Errorf("No container or image specified in configuration")
	}

	return nil
}

func run() error {
	var err error

	// Setup configuration
	err = parseConfig()
	if err != nil {
		return err
	}

	// Watch for configuration changes
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("Unable to setup fsnotify: %s", err)
	}

	err = watcher.Watch(".")
	if err != nil {
		return fmt.Errorf("Unable to setup fsnotify watch: %s", err)
	}

	go func() {
		for {
			select {
			case ev := <-watcher.Event:
				if ev.Name != "./lxd-demo.yaml" {
					continue
				}

				if !ev.IsModify() {
					continue
				}

				fmt.Printf("Reloading configuration\n")
				err := parseConfig()
				if err != nil {
					fmt.Printf("Failed to parse configuration: %s\n", err)
				}
			case err := <-watcher.Error:
				fmt.Printf("Inotify error: %s\n", err)
			}
		}
	}()

	// Connect to the LXD daemon
	lxdDaemon, err = lxd.NewClient(&lxd.DefaultConfig, "local")
	if err != nil {
		return fmt.Errorf("Unable to connect to LXD: %s", err)
	}

	// Setup the database
	err = dbSetup()
	if err != nil {
		return fmt.Errorf("Failed to setup the database: %s", err)
	}

	// Restore cleanup handler for existing containers
	containers, err := dbActive()
	if err != nil {
		return fmt.Errorf("Unable to read current containers: %s", err)
	}

	for _, entry := range containers {
		containerID := int64(entry[0].(int))
		containerName := entry[1].(string)
		containerExpiry := int64(entry[2].(int))

		duration := containerExpiry - time.Now().Unix()
		timeDuration, err := time.ParseDuration(fmt.Sprintf("%ds", duration))
		if err != nil || duration <= 0 {
			lxdForceDelete(lxdDaemon, containerName)
			dbExpire(containerID)
			continue
		}

		time.AfterFunc(timeDuration, func() {
			lxdForceDelete(lxdDaemon, containerName)
			dbExpire(containerID)
		})
	}

	// Setup the HTTP server
	r := mux.NewRouter()
	r.HandleFunc("/1.0", restStatusHandler)
	r.HandleFunc("/1.0/console", restConsoleHandler)
	r.HandleFunc("/1.0/feedback", restFeedbackHandler)
	r.HandleFunc("/1.0/info", restInfoHandler)
	r.HandleFunc("/1.0/start", restStartHandler)
	r.HandleFunc("/1.0/terms", restTermsHandler)

	err = http.ListenAndServe(config.ServerAddr, r)
	if err != nil {
		return err
	}

	return nil
}
