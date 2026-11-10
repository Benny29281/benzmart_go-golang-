package config

import (
	"fmt"
	"net"
	"os"
	"strings"
)

type Config struct {
	HTTPAddr     string
	MySQLDSN     string
	KafkaBrokers []string
	RedisAddr    string
	JWTSecret    string
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:  value("HTTP_ADDR", ":8080"),
		MySQLDSN:  os.Getenv("MYSQL_DSN"),
		RedisAddr: value("REDIS_ADDR", "localhost:6379"),
		JWTSecret: os.Getenv("JWT_SECRET"),
	}
	if c.MySQLDSN == "" {
		return Config{}, fmt.Errorf("MYSQL_DSN wajib diisi")
	}
	for _, address := range strings.Split(value("KAFKA_BROKERS", "localhost:9092"), ",") {
		address = strings.TrimSpace(address)
		if _, _, err := net.SplitHostPort(address); err != nil {
			return Config{}, fmt.Errorf("KAFKA_BROKERS harus berisi host:port: %w", err)
		}
		c.KafkaBrokers = append(c.KafkaBrokers, address)
	}
	return c, nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
