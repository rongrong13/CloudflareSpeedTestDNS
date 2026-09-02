package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lyxot/CloudflareSpeedTestDNS/utils"
)

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

func uploadToGist(token string, files map[string]string) (string, error) {
	gist := GistRequest{
		Description: "Cloudflare CDN Speed Test Results",
		Public:      false,
		Files:       make(map[string]GistFile),
	}
	for filename, content := range files {
		gist.Files[filename] = GistFile{Content: content}
	}
	if len(gist.Files) == 0 {
		return "", fmt.Errorf("no result files to upload")
	}

	jsonData, err := json.Marshal(gist)
	if err != nil {
		return "", fmt.Errorf("JSON 编码失败: %v", err)
	}

	req, err := http.NewRequest("POST", "https://api.github.com/gists", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("Gist 创建失败 (%d): %s", resp.StatusCode, string(body))
	}

	var gistResp GistResponse
	if err := json.Unmarshal(body, &gistResp); err != nil {
		return "", fmt.Errorf("解析响应失败: %v", err)
	}
	return gistResp.HTMLURL, nil
}

// extractIPsFromCSV 从 CSV 文件中提取第一列（IP 地址），每行一个，最多50个
func extractIPsFromCSV(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}

	reader := csv.NewReader(strings.NewReader(string(content)))
	records, err := reader.ReadAll()
	if err != nil {
		return "", err
	}

	var ips []string
	for i, record := range records {
		if i == 0 { // 跳过表头
			continue
		}
		if len(record) > 0 && record[0] != "" {
			ips = append(ips, record[0])
		}
		if len(ips) >= 50 { // 最多保留50个IP
			break
		}
	}
	if len(ips) == 0 {
		return "", nil
	}
	return strings.Join(ips, "\n") + "\n", nil
}

func collectResultFiles() map[string]string {
	files := make(map[string]string)
	candidates := []string{utils.Output}
	for _, name := range []string{"result.csv", "result_ipv4.csv", "result_ipv6.csv"} {
		candidates = append(candidates, name)
	}
	seen := make(map[string]bool)
	for _, path := range candidates {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true

		// 从 CSV 提取纯 IP 列表
		ipList, err := extractIPsFromCSV(path)
		if err != nil || ipList == "" {
			continue
		}

		// 文件名：result.csv → ips.txt, result_ipv4.csv → ips_ipv4.txt, result_ipv6.csv → ips_ipv6.txt
		base := strings.TrimSuffix(filepath.Base(path), ".csv")
		gistName := strings.Replace(base, "result", "ips", 1) + ".txt"
		files[gistName] = ipList
	}
	return files
}

func uploadResultsIfNeeded(token string) {
	if token == "" {
		fmt.Println("未配置 GITHUB_TOKEN，跳过 Gist 上传")
		return
	}
	files := collectResultFiles()
	if len(files) == 0 {
		fmt.Println("未找到测速结果文件，跳过 Gist 上传")
		return
	}
	url, err := uploadToGist(token, files)
	if err != nil {
		fmt.Printf("上传到 Gist 失败: %v\n", err)
		return
	}
	fmt.Printf("✅ 测速结果已上传到 Gist: %s\n", url)
}
