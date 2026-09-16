package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

const (
	DefaultDistributorUID = 4870
)

func main() {
	fmt.Println("=== 正在连接本地数据库 ===")
	common.IsMasterNode = true
	if err := model.InitDB(); err != nil {
		fmt.Printf("数据库初始化失败: %v\n", err)
		os.Exit(1)
	}
	if err := model.DB.AutoMigrate(&model.User{}); err != nil {
		fmt.Printf("User 表迁移失败: %v\n", err)
		os.Exit(1)
	}

	distributorUID := DefaultDistributorUID
	cookie := strings.TrimSpace(os.Getenv("SUBROUTER_COOKIE"))
	if len(os.Args) > 1 && cookie == "" {
		cookie = strings.TrimSpace(os.Args[1])
	}

	var records []service.SubRouterCustomerRecord
	backupPath := "subrouter_customers_backup.json"

	if data, err := os.ReadFile(backupPath); err == nil && len(data) > 0 {
		fmt.Printf("检测到本地快照文件 %s，正在从快照加载客户数据...\n", backupPath)
		if unmarshalErr := common.Unmarshal(data, &records); unmarshalErr != nil {
			fmt.Printf("解析本地快照失败: %v，将尝试在线拉取\n", unmarshalErr)
			records = nil
		}
	}

	var result *service.SyncCustomersResult
	if len(records) > 0 {
		fmt.Printf("从本地快照成功读取到 %d 条客户记录，正在写入数据库...\n", len(records))
		result = service.SyncCustomersFromRecords(records)
	} else {
		if cookie == "" {
			fmt.Println("错误: 未找到本地快照，且未提供 SUBROUTER_COOKIE 环境变量或命令行参数。")
			fmt.Println("用法: go run ./cmd/sync_customers [SUBROUTER_COOKIE]")
			os.Exit(1)
		}
		fmt.Println("=== 正在从 SubRouter 分销商接口拉取客户数据并同步入库 ===")
		var fetchErr error
		result, records, fetchErr = service.SyncSubRouterCustomers(cookie, distributorUID)
		if fetchErr != nil {
			fmt.Printf("同步失败: %v\n", fetchErr)
			os.Exit(1)
		}
		// Save snapshot backup
		if backupBytes, err := common.Marshal(records); err == nil {
			if wErr := os.WriteFile(backupPath, backupBytes, 0644); wErr == nil {
				fmt.Printf("✓ 历史客户原始快照已保存至本地: %s (共 %d 条记录)\n", backupPath, len(records))
			}
		}
	}

	fmt.Println("=== 同步完成 ===")
	fmt.Printf("总计客户记录: %d\n", result.TotalFetched)
	fmt.Printf("新写入客户数: %d\n", result.TotalInserted)
	fmt.Printf("更新已有客户: %d\n", result.TotalUpdated)
	fmt.Printf("失败记录数:   %d\n", result.TotalFailed)

	var totalInDB int64
	model.DB.Model(&model.User{}).Count(&totalInDB)
	fmt.Printf("✓ 当前数据库用户总数 (users 表): %d\n", totalInDB)
}
