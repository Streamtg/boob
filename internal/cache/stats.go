package cache

import (
	"fmt"
	"time"

	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/types"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StatsCache struct {
	db  *gorm.DB
	log *zap.Logger
}

var statsCache *StatsCache

func InitStatsCache(log *zap.Logger) {
	log = log.Named("stats_cache")
	
	db := database.GetDB() // No longer undefined
	if db == nil {
		log.Fatal("Critical: Database provider returned nil")
		return
	}
	
	statsCache = &StatsCache{
		db:  db,
		log: log,
	}
}

func GetStatsCache() *StatsCache {
	return statsCache
}

func (sc *StatsCache) RecordFileProcessed(fileSize int64) error {
	if sc == nil || sc.db == nil { return fmt.Errorf("cache uninitialized") }

	today := time.Now().Truncate(24 * time.Hour)

	return sc.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "date"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"file_count": gorm.Expr("file_count + ?", 1),
			"total_size": gorm.Expr("total_size + ?", fileSize),
		}),
	}).Create(&types.Stats{
		Date:      today,
		FileCount: 1,
		TotalSize: fileSize,
	}).Error
}

func (sc *StatsCache) GetTodayStats() (types.DailyStats, error) {
	today := time.Now().Truncate(24 * time.Hour)
	var stats types.Stats
	err := sc.db.Where("date = ?", today).First(&stats).Error
	if err == gorm.ErrRecordNotFound {
		return types.DailyStats{Date: today}, nil
	}
	return types.DailyStats{Date: stats.Date, FileCount: stats.FileCount, TotalSize: stats.TotalSize}, err
}

func (sc *StatsCache) GetYesterdayStats() (types.DailyStats, error) {
	yesterday := time.Now().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	var stats types.Stats
	err := sc.db.Where("date = ?", yesterday).First(&stats).Error
	if err == gorm.ErrRecordNotFound {
		return types.DailyStats{Date: yesterday}, nil
	}
	return types.DailyStats{Date: stats.Date, FileCount: stats.FileCount, TotalSize: stats.TotalSize}, err
}

func (sc *StatsCache) GetLastWeekStats() (types.WeeklyStats, error) {
	endDate := time.Now().Truncate(24 * time.Hour)
	startDate := endDate.AddDate(0, 0, -7)
	var result struct { FileCount int64; TotalSize int64 }
	err := sc.db.Model(&types.Stats{}).
		Select("COALESCE(SUM(file_count), 0), COALESCE(SUM(total_size), 0)").
		Where("date >= ? AND date < ?", startDate, endDate).
		Row().Scan(&result.FileCount, &result.TotalSize)
	
	return types.WeeklyStats{StartDate: startDate, EndDate: endDate, FileCount: result.FileCount, TotalSize: result.TotalSize}, err
}

func (sc *StatsCache) GetTotalStats() (types.DailyStats, error) {
	var result struct { FileCount int64; TotalSize int64 }
	err := sc.db.Model(&types.Stats{}).
		Select("COALESCE(SUM(file_count), 0), COALESCE(SUM(total_size), 0)").
		Row().Scan(&result.FileCount, &result.TotalSize)
	return types.DailyStats{Date: time.Now(), FileCount: result.FileCount, TotalSize: result.TotalSize}, err
}

func (sc *StatsCache) GetCompleteStats() (types.StatisticsResponse, error) {
	today, _ := sc.GetTodayStats()
	yesterday, _ := sc.GetYesterdayStats()
	lastWeek, _ := sc.GetLastWeekStats()
	total, _ := sc.GetTotalStats()
	return types.StatisticsResponse{Today: today, Yesterday: yesterday, LastWeek: lastWeek, Total: total}, nil
}
