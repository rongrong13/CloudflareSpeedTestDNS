package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Lyxot/CloudflareSpeedTestDNS/utils"
)

var gistMu sync.Mutex // 防止并发上传创建多个 Gist

const (
	gistFileName = "ips.txt"                            // Gist 中的文件名（固定）
	gistDesc     = "CloudflareSpeedLocalTest - IP List" // Gist 描述
)

// getGistIDFile 返回 Gist ID 持久化文件路径
// 优先环境变量 GIST_ID_FILE，默认为当前运行目录下的 .gist_id
func getGistIDFile() string {
	if v := os.Getenv("GIST_ID_FILE"); v != "" {
		return v
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ".gist_id"
	}
	return filepath.Join(cwd, ".gist_id")
}

type GistFile struct {
	Content string `json:"content"`
}

type GistRequest struct {
	Description string              `json:"description"`
	Public      bool                `json:"public"`
	Files       map[string]GistFile `json:"files"`
}

type GistResponse struct {
	HTMLURL string `json:"html_url"`
	ID      string `json:"id"`
}

// readGistID 从本地文件读取已保存的 Gist ID
func readGistID() string {
	data, err := os.ReadFile(getGistIDFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// saveGistID 保存 Gist ID 到本地文件
func saveGistID(id string) {
	f := getGistIDFile()
	if err := os.WriteFile(f, []byte(id), 0644); err != nil {
		fmt.Printf("ERROR: 保存 Gist ID 失败: %v (路径: %s)\n", err, f)
		return
	}
	fmt.Printf("OK: Gist ID 已保存: %s -> %s\n", id, f)
}

// deleteGist 删除指定 ID 的 Gist
func deleteGist(token, gistID string) error {
	req, err := http.NewRequest("DELETE", "https://api.github.com/gists/"+gistID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// updateOrCreateGist 更新已有的 Gist，或创建新的
// 核心逻辑：始终只有一个 Gist 文件 ips.txt，内容为每行一个 IP
func updateOrCreateGist(token string, ipContent string) (string, error) {
	files := map[string]GistFile{gistFileName: {Content: ipContent}}

	gistID := readGistID()

	// 如果有已保存的 Gist ID，尝试更新
	if gistID != "" {
		url, err := patchGist(token, gistID, files)
		if err == nil {
			utils.LogInfo("Gist 更新成功: %s", url)
			return url, nil
		}
		utils.LogWarn("更新 Gist 失败（将创建新的）: %v", err)
	}

	// 创建新的 Gist
	url, id, err := createGist(token, files)
	if err != nil {
		return "", err
	}
	saveGistID(id)
	utils.LogInfo("Gist 创建成功: %s", url)
	return url, nil
}

// patchGist 更新已有 Gist 的内容
func patchGist(token string, gistID string, files map[string]GistFile) (string, error) {
	gist := GistRequest{
		Description: gistDesc,
		Files:       files,
	}
	jsonData, _ := json.Marshal(gist)

	req, err := http.NewRequest("PATCH", "https://api.github.com/gists/"+gistID, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PATCH 失败 (%d): %s", resp.StatusCode, string(body))
	}

	var r GistResponse
	json.Unmarshal(body, &r)
	return r.HTMLURL, nil
}

// createGist 创建新的 Gist
func createGist(token string, files map[string]GistFile) (string, string, error) {
	gist := GistRequest{
		Description: gistDesc,
		Public:      false,
		Files:       files,
	}
	jsonData, _ := json.Marshal(gist)

	req, err := http.NewRequest("POST", "https://api.github.com/gists", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("创建失败 (%d): %s", resp.StatusCode, string(body))
	}

	var r GistResponse
	json.Unmarshal(body, &r)
	return r.HTMLURL, r.ID, nil
}

// uploadIPsToGist 接收 IP 列表（每行一个），上传到 Gist
// 这是 /api/gist 端点调用的函数
func uploadIPsToGist(token string, ipContent string) (string, error) {
	gistMu.Lock()
	defer gistMu.Unlock()
	if token == "" {
		return "", fmt.Errorf("未配置 GITHUB_TOKEN")
	}
	if strings.TrimSpace(ipContent) == "" {
		return "", fmt.Errorf("IP 列表为空")
	}
	return updateOrCreateGist(token, ipContent)
}

// uploadResultsIfNeeded CLI 模式：从 CSV 提取 IP 上传到 Gist
func uploadResultsIfNeeded(token string) {
	if token == "" {
		fmt.Println("未配置 GITHUB_TOKEN，跳过 Gist 上传")
		return
	}
	// 读取实际输出文件
	outputFile := utils.Output
	if outputFile == "" {
		outputFile = "result.csv"
	}
	content, err := os.ReadFile(outputFile)
	if err != nil {
		fmt.Printf("读取结果文件失败: %v\n", err)
		return
	}
	// 简单解析 CSV，提取第一列 IP
	lines := strings.Split(string(content), "\n")
	var ips []string
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // 跳过表头和空行
		}
		parts := strings.SplitN(line, ",", 2)
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			ips = append(ips, strings.TrimSpace(parts[0]))
		}
		if len(ips) >= 50 {
			break
		}
	}
	if len(ips) == 0 {
		fmt.Println("未找到 IP 结果，跳过 Gist 上传")
		return
	}
	ipContent := strings.Join(ips, "\n") + "\n"
	url, err := uploadIPsToGist(token, ipContent)
	if err != nil {
		fmt.Printf("上传到 Gist 失败: %v\n", err)
		return
	}
	fmt.Printf("✅ 测速结果已上传到 Gist: %s\n", url)
}

// cleanupOldGists 清理描述匹配的旧 Gist（可选，用于批量清理）
func cleanupOldGists(token string) int {
	req, _ := http.NewRequest("GET", "https://api.github.com/gists?per_page=100", nil)
	req.Header.Set("Authorization", "token "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var gists []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}
	json.Unmarshal(body, &gists)

	deleted := 0
	savedID := readGistID()
	for _, g := range gists {
		if g.ID == savedID {
			continue // 跳过当前使用的 Gist
		}
		if strings.Contains(g.Description, "CloudflareSpeedLocalTest") || strings.Contains(g.Description, "Speed Test Results") {
			deleteGist(token, g.ID)
			deleted++
		}
	}
	return deleted
}
