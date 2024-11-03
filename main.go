package main

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/fbsobreira/gotron-sdk/pkg/address"
	"github.com/shirou/gopsutil/cpu"
	_ "modernc.org/sqlite" // 使用 modernc 的 SQLite
)

// Wallet 结构体，用于存储生成的 TRON 钱包信息
type Wallet struct {
	WIF     string
	Address string
}

// GenerateTRONKey 生成一个 TRON 私钥和地址
func GenerateTRONKey() (wif string, tronAddress string) {
	pri, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		log.Fatal("生成 TRON 私钥失败:", err)
	}

	tronAddress = address.PubkeyToAddress(pri.ToECDSA().PublicKey).String()
	wif = hex.EncodeToString(pri.D.Bytes())

	return wif, tronAddress
}

// saveWalletBatch 将生成的钱包信息批量保存到 SQLite 数据库
func saveWalletBatch(db *sql.DB, wallets []Wallet) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO tron_wallets (wif, address) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, wallet := range wallets {
		_, err = stmt.Exec(wallet.WIF, wallet.Address)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// 动态调整生成速率，并输出当前负载信息
func adjustRate(goroutines *int, targetLoad float64, createdWallets *int, stop chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			load, _ := cpu.Percent(time.Second, false)
			currentLoad := load[0]

			if currentLoad < targetLoad-10 && *goroutines < 100 {
				*goroutines++
			} else if currentLoad > targetLoad+10 && *goroutines > 1 {
				*goroutines--
			}

			fmt.Printf("当前 CPU 负载：%.2f%%, Goroutines 数量：%d, 创建的钱包数量：%d\n", currentLoad, *goroutines, *createdWallets)
		}
	}
}

func main() {
	db, err := sql.Open("sqlite", "./tron_wallets.db")
	if err != nil {
		log.Fatal("无法连接到数据库:", err)
	}
	defer db.Close()

	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS tron_wallets (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            wif TEXT NOT NULL,
            address TEXT NOT NULL
        );
    `)
	if err != nil {
		log.Fatal("无法创建表:", err)
	}

	goroutines := 5    // 初始 Goroutine 数量
	targetLoad := 70.0 // 目标 CPU 使用率
	createdWallets := 0
	stop := make(chan struct{})
	walletChannel := make(chan Wallet, 100)

	// 处理退出信号
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-signalChannel
		fmt.Println("\n收到退出信号，正在退出...")
		close(stop)
		close(walletChannel)
	}()

	var wg sync.WaitGroup
	go adjustRate(&goroutines, targetLoad, &createdWallets, stop)

	// 动态启动 Goroutines 生成钱包
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					tronWif, tronAddr := GenerateTRONKey()
					walletChannel <- Wallet{WIF: tronWif, Address: tronAddr}
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(walletChannel)
	}()

	// 监听 walletChannel 并批量保存钱包
	var wallets []Wallet
	for wallet := range walletChannel {
		wallets = append(wallets, wallet)
		createdWallets++

		if len(wallets) >= 100 {
			if err := saveWalletBatch(db, wallets); err != nil {
				log.Fatal("无法保存钱包信息到数据库:", err)
			}
			wallets = wallets[:0] // 清空切片
		}
	}

	// 最后批量保存
	if len(wallets) > 0 {
		if err := saveWalletBatch(db, wallets); err != nil {
			log.Fatal("无法保存钱包信息到数据库:", err)
		}
	}

	fmt.Println("程序已安全退出。")
}
