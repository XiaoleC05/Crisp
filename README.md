# Crisp

Agent 输出质量门禁（Quality Gate）：在 Agent 回答交付用户之前自动检查并拦截低质量输出。

> 状态：MVP 开发中。第一版只接入 Codex CLI Stop Hook。
> 完整设计见 [docs/design.md](docs/design.md)。

## 构建

`~/powershell
go build -o bin/crisp.exe ./cmd/crisp
`~

## 本地检查

`~/powershell
go vet ./...
go test ./...
`~
