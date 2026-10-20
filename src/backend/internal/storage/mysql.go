package storage

import (
	"context"
	"net"
	"strconv"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"workflow-api/internal/config"
)

func OpenMySQL(cfg config.DatabaseConfig) (*gorm.DB, error) {
	return gorm.Open(gormmysql.Open(buildMySQLDSN(cfg)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
}

func buildMySQLDSN(cfg config.DatabaseConfig) string {
	return (&mysqlDriver.Config{
		User:                 cfg.User,
		Passwd:               cfg.Password,
		Net:                  "tcp",
		Addr:                 net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		DBName:               cfg.Name,
		Params:               map[string]string{"charset": "utf8mb4"},
		ParseTime:            true,
		Loc:                  time.UTC,
		AllowNativePasswords: true,
		TLSConfig:            "preferred",
	}).FormatDSN()
}

func PingMySQL(db *gorm.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
