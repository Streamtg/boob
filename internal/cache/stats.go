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

// InitStatsCache initializes the global stats singleton
func InitStatsCache(log *zap.Logger) {
	log = log.Named("stats_cache")
	
	db := database.GetDB()
	if db == nil {
		log.Fatal("Database not initialized: cannot start stats cache")
		return
	}
	
	statsCache = &StatsCache{
		db:  db,
		log: log,
	}
	log.Info("Stats cache successfully linked to GORM provider")
}

// GetStatsCache returns the singleton instance
func GetStatsCache() *StatsCache {
	return statsCache
}

// RecordFileProcessed updates daily stats using an atomic Upsert pattern
func (sc *StatsCache) RecordFileProcessed(fileSize int64) error {
	if sc == nil || sc.db == nil {
		return fmt.Errorf("stats cache not initialized")
	}

	today := time.Now().Truncate(24 * time.Hour)

	// Senior Note: We use OnConflict (Upsert) to handle concurrency safely.
	// This updates the existing row if the date exists, or creates it if not.
	err := sc.db.Clauses(clause.OnConflict{
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

	if err != nil {
		sc.log.Error("Failed to record file processing", zap.Error(err))
		return err
	}
	return nil
}

// GetTodayStats retrieves records for the current UTC day
func (sc *StatsCache) GetTodayStats() (types.DailyStats, error) {
	today := time.Now().Truncate(24 * time.Hour)
	var stats types.Stats
	
	err := sc.db.Where("date = ?", today).First(&stats).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return types.DailyStats{Date: today}, nil
		}
		return types.DailyStats{}, err
	}
	
	return types.DailyStats{
		Date:      stats.Date,
		FileCount: stats.FileCount,
		TotalSize: stats.TotalSize,
	}, nil
}

// GetYesterdayStats retrieves records for the previous UTC day
func (sc *StatsCache) GetYesterdayStats() (types.DailyStats, error) {
	yesterday := time.Now().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	var stats types.Stats
	
	err := sc.db.Where("date = ?", yesterday).First(&stats).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return types.DailyStats{Date: yesterday}, nil
		}
		return types.DailyStats{}, err
	}
	
	return types.DailyStats{
		Date:      stats.Date,
		FileCount: stats.FileCount,
		TotalSize: stats.TotalSize,
	}, nil
}

// GetLastWeekStats aggregates data for the rolling last 7 days
func (sc *StatsCache) GetLastWeekStats() (types.WeeklyStats, error) {
	endDate := time.Now().Truncate(24 * time.Hour)
	startDate := endDate.AddDate(0, 0, -7)
	
	var result struct {
		FileCount int64
		TotalSize int64
	}
	
	err := sc.db.Model(&types.Stats{}).
		Select("COALESCE(SUM(file_count), 0), COALESCE(SUM(total_size), 0)").
		Where("date >= ? AND date < ?", startDate, endDate).
		Row().Scan(&result.FileCount, &result.TotalSize)
	
	if err != nil {
		return types.WeeklyStats{}, err
	}
	
	return types.WeeklyStats{
		StartDate: startDate,
		EndDate:   endDate,
		FileCount: result.FileCount,
		TotalSize: result.TotalSize,
	}, nil
}

// GetTotalStats aggregates all-time data
func (sc *StatsCache) GetTotalStats() (types.DailyStats, error) {
	var result struct {
		FileCount int64
		TotalSize int64
	}
	
	err := sc.db.Model(&types.Stats{}).
		Select("COALESCE(SUM(file_count), 0), COALESCE(SUM(total_size), 0)").
		Row().Scan(&result.FileCount, &result.TotalSize)
	
	if err != nil {
		return types.DailyStats{}, err
	}
	
	return types.DailyStats{
		Date:      time.Now(),
		FileCount: result.FileCount,
		TotalSize: result.TotalSize,
	}, nil
}

// GetCompleteStats returns a unified response for the dashboard
func (sc *StatsCache) GetCompleteStats() (types.StatisticsResponse, error) {
	today, _ := sc.GetTodayStats()
	yesterday, _ := sc.GetYesterdayStats()
	lastWeek, _ := sc.GetLastWeekStats()
	total, _ := sc.GetTotalStats()
	
	return types.StatisticsResponse{
		Today:     today,
		Yesterday: yesterday,
		LastWeek:  lastWeek,
		Total:     total,
	}, nil
}
