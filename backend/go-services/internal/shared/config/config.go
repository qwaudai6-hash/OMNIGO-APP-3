package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// Config holds the common configuration for a microservice
type Config struct {
	Port             int
	Env              string
	DBWriterDSN      string   `mapstructure:"db_writer_dsn"`
	DBReaderDSN      string   `mapstructure:"db_reader_dsn"`
	RedisAddrs       []string `mapstructure:"redis_addrs"`
	KafkaBrokers     []string `mapstructure:"kafka_brokers"`
	OSRMURL          string   `mapstructure:"osrm_url"`
	SentryDSN        string   `mapstructure:"sentry_dsn"`
	MapLibreAPIKey   string   `mapstructure:"maplibre_api_key"`
	MapLibreStyleURL string   `mapstructure:"maplibre_style_url"`
	S3Endpoint       string   `mapstructure:"s3_endpoint"`
	S3AccessKey      string   `mapstructure:"s3_access_key"`
	S3SecretKey      string   `mapstructure:"s3_secret_key"`
	S3Bucket         string   `mapstructure:"s3_bucket"`
	S3Region         string   `mapstructure:"s3_region"`
	S3UsePathStyle   bool     `mapstructure:"s3_use_path_style"`
	S3PublicURL      string   `mapstructure:"s3_public_url"`
}

// LoadConfig reads configuration from file or environment variables.
// PANICS if critical env vars are missing — no silent localhost fallbacks.
func LoadConfig(path string) *Config {
	viper.AddConfigPath(path)
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	_ = viper.ReadInConfig() // ignore missing file

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		cfg = Config{} // start fresh on unmarshal error
	}

	// ── Database (REQUIRED — no hardcoded fallback) ──
	if cfg.DBWriterDSN == "" {
		cfg.DBWriterDSN = envOrFirst("DATABASE_URL", "DB_WRITER_DSN")
	}
	if cfg.DBReaderDSN == "" {
		cfg.DBReaderDSN = envOrFirst("DATABASE_URL", "DB_READER_DSN")
	}
	if cfg.DBWriterDSN == "" {
		panic("FATAL: DATABASE_URL (or DB_WRITER_DSN) is required. Refusing to start with localhost fallback.")
	}
	if cfg.DBReaderDSN == "" {
		cfg.DBReaderDSN = cfg.DBWriterDSN
	}

	// ── Redis (optional — only services that use Redis must validate it) ──
	if len(cfg.RedisAddrs) == 0 {
		env := os.Getenv("REDIS_ADDRS")
		if env != "" {
			cfg.RedisAddrs = strings.Split(env, ",")
		}
	}

	// ── Kafka (optional — some services don't need it) ──
	if len(cfg.KafkaBrokers) == 0 {
		env := os.Getenv("KAFKA_BROKERS")
		if env != "" {
			cfg.KafkaBrokers = strings.Split(env, ",")
		}
	}

	// ── OSRM (optional — only delivery/ride services need it) ──
	if cfg.OSRMURL == "" {
		cfg.OSRMURL = os.Getenv("OSRM_URL")
		if cfg.OSRMURL == "" {
			cfg.OSRMURL = os.Getenv("OSRM_BASE_URL")
		}
	}

	if cfg.Env == "" {
		cfg.Env = os.Getenv("APP_ENV")
		if cfg.Env == "" {
			cfg.Env = "production"
		}
	}

	// Sentry (optional)
	if cfg.SentryDSN == "" {
		cfg.SentryDSN = os.Getenv("SENTRY_DSN")
	}

	// Map service config (optional — only map-service needs it)
	if cfg.MapLibreAPIKey == "" {
		cfg.MapLibreAPIKey = os.Getenv("MAPLIBRE_API_KEY")
	}
	if cfg.MapLibreStyleURL == "" {
		cfg.MapLibreStyleURL = os.Getenv("MAPLIBRE_STYLE_URL")
		if cfg.MapLibreStyleURL == "" && cfg.MapLibreAPIKey != "" {
			cfg.MapLibreStyleURL = "https://api.maptiler.com/maps/streets/style.json?key=" + cfg.MapLibreAPIKey
		}
	}

	// S3 / MinIO config (optional — only file upload services need it)
	if cfg.S3Endpoint == "" {
		cfg.S3Endpoint = envOrFirst("S3_ENDPOINT", "AWS_ENDPOINT_URL")
	}
	if cfg.S3AccessKey == "" {
		cfg.S3AccessKey = envOrFirst("S3_ACCESS_KEY", "AWS_ACCESS_KEY_ID")
	}
	if cfg.S3SecretKey == "" {
		cfg.S3SecretKey = envOrFirst("S3_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")
	}
	if cfg.S3Bucket == "" {
		cfg.S3Bucket = envOrFirst("S3_BUCKET", "AWS_BUCKET")
	}
	if cfg.S3Region == "" {
		cfg.S3Region = envOrFirst("S3_REGION", "AWS_REGION")
		if cfg.S3Region == "" {
			cfg.S3Region = "us-east-1"
		}
	}
	if cfg.S3PublicURL == "" {
		cfg.S3PublicURL = os.Getenv("S3_PUBLIC_URL")
	}
	if v := os.Getenv("S3_USE_PATH_STYLE"); v != "" {
		cfg.S3UsePathStyle, _ = strconv.ParseBool(v)
	}

	return &cfg
}

// EnvPort returns the PORT env var if set and valid, otherwise def.
func EnvPort(def int) int {
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p != 0 {
			return p
		}
	}
	return def
}

// envOrFirst tries env vars in order, returns first non-empty or "".
func envOrFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
