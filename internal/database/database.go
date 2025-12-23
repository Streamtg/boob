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

var ValueOf = &config{}

type config struct {
	// Telegram & Network
	APIID           int64    `envconfig:"API_ID" required:"true"`
	APIHash         string   `envconfig:"API_HASH" required:"true"`
	BotToken        string   `envconfig:"BOT_TOKEN" required:"true"`
	LogChannelID    int64    `envconfig:"LOG_CHANNEL" required:"true"`
	Host            string   `envconfig:"HOST"`
	Port            int      `envconfig:"PORT" default:"8080"`
	WorkerURL       string   `envconfig:"WORKER_URL" required:"true"`
	
	// Cache Management
	MaxCacheSize    int64    `envconfig:"MAX_CACHE_SIZE" default:"10737418240"` // 10GB
	CacheDirectory  string   `envconfig:"CACHE_DIRECTORY" default:".cache"`
	
	// GitHub Persistence Layer
	GithubOwner     string   `envconfig:"GITHUB_OWNER"`
	GithubRepo      string   `envconfig:"GITHUB_REPO"`
	GithubDbPath    string   `envconfig:"GITHUB_DB_PATH" default:"storage/database.json"`
	GithubToken     string   `envconfig:"GITHUB_TOKEN"` // Critical for API access
	
	// Permissions & Other
	AllowedUsers    []int64  `envconfig:"ALLOWED_USERS"`
	ForceSubChannel string   `envconfig:"FORCE_SUB_CHANNEL"`
	HashLength      int      `envconfig:"HASH_LENGTH" default:"6"`
	UsePublicIP     bool     `envconfig:"USE_PUBLIC_IP" default:"false"`
	MultiTokens     []string
}

// ... rest of the helper functions remain the same to maintain build stability ...

func (c *config) setupEnvVars(log *zap.Logger, cmd *cobra.Command) {
	c.loadFromEnvFile(log)
	
	err := envconfig.Process("", c)
	if err != nil {
		log.Fatal("Error processing env vars", zap.Error(err))
	}

	// Dynamic IP Setup
	ip, _ := getIP(c.UsePublicIP)
	if c.Host == "" {
		c.Host = "http://" + ip + ":" + strconv.Itoa(c.Port)
	}
}

func Load(log *zap.Logger, cmd *cobra.Command) {
	log = log.Named("Config")
	ValueOf.setupEnvVars(log, cmd)
	
	// Normalize LogChannelID
	ValueOf.LogChannelID = int64(stripInt(log, int(ValueOf.LogChannelID)))
	log.Info("Host Wave Config Initialized with GitHub Persistence Support")
}

// Internal IP and Public IP helpers (Your established logic)
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
