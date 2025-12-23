package config

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// ValueOf is the globally accessible configuration instance
var ValueOf = &config{}

type config struct {
	// Telegram & Core
	APIID           int64    `envconfig:"API_ID" required:"true"`
	APIHash         string   `envconfig:"API_HASH" required:"true"`
	BotToken        string   `envconfig:"BOT_TOKEN" required:"true"`
	LogChannelID    int64    `envconfig:"LOG_CHANNEL" required:"true"`
	Host            string   `envconfig:"HOST"`
	Port            int      `envconfig:"PORT" default:"8080"`
	WorkerURL       string   `envconfig:"WORKER_URL" required:"true"`

	// Cache & Storage
	MaxCacheSize    int64    `envconfig:"MAX_CACHE_SIZE" default:"10737418240"`
	CacheDirectory  string   `envconfig:"CACHE_DIRECTORY" default:".cache"`
	
	// GitHub Persistence
	GithubOwner     string   `envconfig:"GITHUB_OWNER"`
	GithubRepo      string   `envconfig:"GITHUB_REPO"`
	GithubDbPath    string   `envconfig:"GITHUB_DB_PATH" default:"storage/database.json"`
	GithubToken     string   `envconfig:"GITHUB_TOKEN"`

	// Permissions & Security
	AllowedUsers    []int64  `envconfig:"ALLOWED_USERS"`
	ForceSubChannel string   `envconfig:"FORCE_SUB_CHANNEL"`
	HashLength      int      `envconfig:"HASH_LENGTH" default:"6"`
	UsePublicIP     bool     `envconfig:"USE_PUBLIC_IP" default:"false"`
	
	// Internal
	MultiTokens     []string
}

var botTokenRegex = regexp.MustCompile(`MULTI\_TOKEN\d+=(.*)`)

// loadFromEnvFile loads the .env file into the system environment
func (c *config) loadFromEnvFile(log *zap.Logger) {
	// Use filepath.Clean for cross-platform compatibility
	envPath := filepath.Clean(".env")
	
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		log.Warn(".env file not found, skipping file load and using system environment only")
		return
	}

	err := godotenv.Load(envPath)
	if err != nil {
		log.Fatal("Error loading .env file", zap.Error(err))
	}
}

// setupEnvVars orchestrates the loading sequence
func (c *config) setupEnvVars(log *zap.Logger, cmd *cobra.Command) {
	// Step 1: Load file into OS environment
	c.loadFromEnvFile(log)

	// Step 2: Map OS environment to the struct
	// We use an empty prefix "" to match keys like API_ID directly
	err := envconfig.Process("", c)
	if err != nil {
		log.Fatal("Error processing environment variables via envconfig", zap.Error(err))
	}

	// Step 3: Handle dynamic network configuration
	ip, _ := getIP(c.UsePublicIP)
	if c.Host == "" {
		c.Host = "http://" + ip + ":" + strconv.Itoa(c.Port)
	}

	// Step 4: Parse multi-token pattern if exists
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "MULTI_TOKEN") {
			match := botTokenRegex.FindStringSubmatch(env)
			if len(match) > 1 {
				c.MultiTokens = append(c.MultiTokens, match[1])
			}
		}
	}
}

// Load is the public entry point called from main.go
func Load(log *zap.Logger, cmd *cobra.Command) {
	log = log.Named("config")
	ValueOf.setupEnvVars(log, cmd)
	
	// Correct the Log Channel ID format (handling the -100 prefix)
	ValueOf.LogChannelID = int64(stripInt(log, int(ValueOf.LogChannelID)))
	
	log.Info("Configuration successfully loaded from environment", 
		zap.String("host", ValueOf.Host),
		zap.String("worker", ValueOf.WorkerURL))
}

// --- Internal Helper Functions ---

func getIP(public bool) (string, error) {
	if public { return GetPublicIP() }
	return getInternalIP()
}

func getInternalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil { return "localhost", err }
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func GetPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org?format=text")
	if err != nil { return "localhost", err }
	defer resp.Body.Close()
	ip, _ := io.ReadAll(resp.Body)
	return string(ip), nil
}

func stripInt(log *zap.Logger, a int) int {
	strA := strconv.Itoa(abs(a))
	lastDigits := strings.Replace(strA, "100", "", 1)
	result, _ := strconv.Atoi(lastDigits)
	return result
}

func abs(x int) int {
	if x < 0 { return -x }
	return x
}
