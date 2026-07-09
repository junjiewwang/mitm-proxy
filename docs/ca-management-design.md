# CA 管理模块设计

## 一、架构总览

```
core/ca/                          ← 新包，CA 全生命周期
├── ca.go              CA 生成/加载/确定性种子
├── ca_test.go         单元测试
├── detector.go        信任检测器接口
├── detector_darwin.go macOS 实现（钥匙串 + Firefox）
├── distributor.go     mobileconfig/cer 分发
├── types.go           接口 + 数据模型

webui/webui.go         → +GET /api/ca/status, +GET /api/ca/download
webui/static/          → 前端 CA 状态卡片
```

## 二、依赖方向

```
webui ──→ core/ca ──→ os/exec (检测钥匙串)
             ↑
        cmd/mitm-proxy (注入确定性种子)
```

**无循环**。`core/ca` 不依赖 webui、gin、rule。

## 三、接口设计

### 3.1 types.go — 共享类型和接口

```go
type TrustStatus struct {
    Name    string `json:"name"`    // "System Keychain", "Firefox"
    Trusted bool   `json:"trusted"` 
    Path    string `json:"path"`    // 安装路径提示
}

// Detector 检测 CA 在各浏览器/OS 中的信任状态
type Detector interface {
    // 检测所有已知的信任存储
    CheckAll(caFingerprint string) []TrustStatus
}

// Distributor 生成各平台的分发文件
type Distributor interface {
    MobileConfig(caPEM []byte) ([]byte, error)  // macOS
    CRTFile(caPEM []byte) []byte                   // 通用 .crt（直接返回原文件）
}
```

### 3.2 ca.go — CA 管理核心

```go
// Manager CA 证书管理器
type Manager struct {
    cert *x509.Certificate
    key  crypto.Signer          // 接口，非具体类型
    pem  []byte                 // CA 证书 PEM 格式
}

// NewManager 加载或生成 CA
// seed: 确定性种子，nil = 随机生成
func NewManager(dir string, seed []byte) (*Manager, error)

// Seed 返回当前使用的种子（用于持久化配置）
func (m *Manager) Seed() []byte

// Fingerprint CA 指纹（用于检测信任状态）
func (m *Manager) Fingerprint() string

// PEM 返回 CA 证书的 PEM 字节
func (m *Manager) PEM() []byte
```

**关键设计**：

```go
// 确定性种子：sha256("yunapi-proxy-v1") → Ed25519 private key
// 任何机器相同种子 → 相同 CA → 相同指纹
func deriveKeyFromSeed(seed []byte) (crypto.Signer, error) {
    hash := sha256.Sum256(seed)
    return ed25519.NewKeyFromSeed(hash[:]), nil
}
```

### 3.3 detector_darwin.go — macOS 检测

```go
type darwinDetector struct{}

func (d *darwinDetector) CheckAll(fp string) []TrustStatus {
    return []TrustStatus{
        checkSystemKeychain(fp),    // security find-certificate
        checkFirefox(fp),           // 检查 ~/Library/Application Support/Firefox/Profiles/*/cert9.db
    }
}
```

**低耦合设计**：检测结果**只读**，不修改系统状态。

### 3.4 detector_other.go — 其他平台 stub

```go
type stubDetector struct{}

func (d *stubDetector) CheckAll(fp string) []TrustStatus {
    return nil // 其他平台暂不实现，优雅降级
}
```

### 3.5 distributor.go — 分发文件

```go
// mobileconfig 模板嵌入（go:embed）
//go:embed trust.mobileconfig.tmpl
var mobileConfigTmpl string

func (d *distributor) MobileConfig(caPEM []byte) ([]byte, error) {
    der := pemToDER(caPEM)
    b64 := base64.StdEncoding.EncodeToString(der)
    return fmt.Sprintf(mobileConfigTmpl, b64), nil
}
```

## 四、API 设计

### GET /api/ca/status

```json
{
    "fingerprint": "SHA256:xxxx",
    "installed": true,           // 任意一个 store 已信任
    "stores": [
        {"name": "System Keychain", "trusted": true, "path": "/Library/Keychains/System.keychain"},
        {"name": "Firefox",         "trusted": false, "path": "about:preferences#privacy"}
    ],
    "files": [
        {"format": "mobileconfig", "url": "/api/ca/download?format=mobileconfig"},
        {"format": "crt",          "url": "/api/ca/download?format=crt"}
    ]
}
```

### GET /api/ca/download?format=mobileconfig|crt

返回文件字节流，`Content-Type` 设为 `application/octet-stream`。

## 五、Web UI 状态卡片

在 Status Tab 顶部添加 CA 状态卡片：

```
┌─ CA 证书状态 ──────────────────────────────────────────┐
│  Fingerprint: SHA256:1ce5e50f...                        │
│                                                         │
│  🟢 System Keychain    ✓ 已信任                         │
│  🔴 Firefox / Zen      ✗ 未导入    [下载 mobileconfig]   │
│                                                         │
│  首次使用？下载 mobileconfig 文件 → 双击 → 点"安装"     │
└─────────────────────────────────────────────────────────┘
```

纯前端，请求 `/api/ca/status` 渲染。

## 六、健壮性设计

| 场景 | 处理 |
|------|------|
| `certs/` 不存在 | 自动创建 + 生成 CA |
| 检测命令超时 | `context.WithTimeout(3s)` |
| 检测命令不可用 | 返回 `trusted: false, error: "..."` 而不是 panic |
| 种子为 nil | 生成随机种子 |
| 并发安全 | `Manager` 用 `sync.Once` 初始化 |
| 文件写入失败 | 返回 error，不静默吞掉 |

## 七、可测试性

| 模块 | 测试方式 |
|------|----------|
| `ca.go` | 纯函数 + mock `os.MkdirAll`/`os.WriteFile` 用 `t.TempDir()` |
| `derivedKeyFromSeed` | 输入固定种子，验证输出指纹可重复 |
| `darwinDetector` | 构建时 tag，CI 上用 stub |
| `distributor` | 输入已知 DER → 验证 mobileconfig XML 结构 |

## 八、配置兼容

`config.yaml` 新增可选字段：

```yaml
ca:
  seed: "yunapi-proxy-v1"   # 可选：确定性种子（空则随机）
```

不填 seed 时仍生成随机 CA，完全向后兼容。

## 九、实施步骤

| 步骤 | 内容 | 工期 |
|:--:|------|:--:|
| 1 | `core/ca/` 包：Manager + 接口 + 确定性种子 + 单测 | 1d |
| 2 | `detector_darwin.go` + stub + 单测 | 0.5d |
| 3 | `distributor.go` + mobileconfig 模板 + 单测 | 0.5d |
| 4 | `GET /api/ca/status` + `GET /api/ca/download` | 0.5d |
| 5 | Web UI 状态卡片 + 下载指引 | 0.5d |
| **合计** | | **3d** |

## 十、不做什么（明确排除）

- ❌ 不自动安装 CA（安全边界，必须有用户确认）
- ❌ 不检测 Chrome（走系统钥匙串，与 System Keychain 等价）
- ❌ 不支持 Windows/Linux 检测（v1 先做 macOS，stub 占位）
- ❌ 不修改现有 `core/proxy/cert.go`（CA 生成逻辑保持原样，新包是增强层）
