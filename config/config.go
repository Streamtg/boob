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
	APIID           int64    `envconfig:"API_ID" required:"true"`
	APIHash         string   `envconfig:"API_HASH" required:"true"`
	BotToken        string   `envconfig:"BOT_TOKEN" required:"true"`
	LogChannelID    int64    `envconfig:"LOG_CHANNEL" required:"true"`
	Host            string   `envconfig:"HOST"`
	Port            int      `envconfig:"PORT" default:"8080"`
	WorkerURL       string   `envconfig:"WORKER_URL" required:"true"`
	MaxCacheSize    int64    `envconfig:"MAX_CACHE_SIZE" default:"10737418240"`
	CacheDirectory  string   `envconfig:"CACHE_DIRECTORY" default:".cache"`
	GithubOwner     string   `envconfig:"GITHUB_OWNER"`
	GithubRepo      string   `envconfig:"GITHUB_REPO"`
	GithubDbPath    string   `envconfig:"GITHUB_DB_PATH" default:"storage/database.json"`
	GithubToken     string   `envconfig:"GITHUB_TOKEN"`
	AllowedUsers    []int64  `envconfig:"ALLOWED_USERS"`
	ForceSubChannel string   `envconfig:"FORCE_SUB_CHANNEL"`
	HashLength      int      `envconfig:"HASH_LENGTH" default:"6"`
	UsePublicIP     bool     `envconfig:"USE_PUBLIC_IP" default:"false"`
	MultiTokens     []string
}

var botTokenRegex = regexp.MustCompile(`MULTI\_TOKEN\d+=(.*)`)

func (c *config) loadFromEnvFile(log *zap.Logger) {
	envPath := filepath.Clean(".env")
	if _, err := os.Stat(envPath); os.IsNotExist(err) {
		log.Warn(".env file not found, using system environment")
		return
	}
	_ = godotenv.Load(envPath)
}

func (c *config) setupEnvVars(log *zap.Logger, cmd *cobra.Command) {
	c.loadFromEnvFile(log)
	if err := envconfig.Process("", c); err != nil {
		log.Fatal("Env processing failed", zap.Error(err))
	}

	ip, _ := getIP(c.UsePublicIP)
	if c.Host == "" {
		c.Host = "http://" + ip + ":" + strconv.Itoa(c.Port)
	}

	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "MULTI_TOKEN") {
			match := botTokenRegex.FindStringSubmatch(env)
			if len(match) > 1 {
				c.MultiTokens = append(c.MultiTokens, match[1])
			}
		}
	}
}

func Load(log *zap.Logger, cmd *cobra.Command) {
	ValueOf.setupEnvVars(log, cmd)
	ValueOf.LogChannelID = int64(stripInt(log, int(ValueOf.LogChannelID)))
}

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
