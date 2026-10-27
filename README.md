<p align="center">
  <img src="https://raw.githubusercontent.com/iCodZilla/defhop/main/media/banner.png" alt="Defhop — Java 代码导航助手" width="720" />
</p>

<p align="center">
  <strong>Defhop</strong> · Java 代码导航助手<br/>
  <em>跳转定义 · 查找引用 · 读代码更快</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Java-导航助手-2EC4B6?style=flat-square" alt="Java 助手" />
  <img src="https://img.shields.io/badge/Engine-Go-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/跳转定义-F12-007ACC?style=flat-square" alt="跳转定义" />
  <img src="https://img.shields.io/badge/查找引用-快-1B1F24?style=flat-square" alt="查找引用" />
  <img src="https://img.shields.io/badge/Spring%20%7C%20MyBatis%20%7C%20Feign-按需-00ADD8?style=flat-square" alt="框架支持" />
</p>

<p align="center">
  <a href="#-中文"><img src="https://img.shields.io/badge/中文-说明-2EC4B6?style=for-the-badge" alt="中文" /></a>
  &nbsp;
  <a href="#-english"><img src="https://img.shields.io/badge/English-Guide-1B1F24?style=for-the-badge" alt="English" /></a>
</p>

---

# 中文

## 这是什么

**Defhop** = **Java 助手**，专注一件事：**代码导航**。

- **跳转定义**（Go to Definition / `F12`）
- **查找引用**（Find References）
- 方法实现、继承关系
- MyBatis / Spring / Feign / Lombok / MapStruct 等场景的跨文件跳转

适合在大项目里 **看代码、追调用、读懂别人写的逻辑**——尤其是你已经用 AI 写代码，只想要一个又快又轻的「跳过去」工具。

核心引擎用 **Go** 编写：原生多核解析 Java 源码，**不依赖 JVM** 跑语言服务，体积小、内存省、跳转快。

> 不做代码补全、不做重构、不做一堆 IDE 全家桶。保持精简，打开快、跳得快。

## 由来

AI 时代，写代码越来越少靠人肉敲：生成、改写、补洞多半交给助手。以前视为标配的 **代码提示、重构**，反而常常变成编辑器里的负担——又重、又抢资源，还和 AI 能力大量重叠。

但人还是要 **偶尔看代码**：追调用、核对实现、弄清某段逻辑从哪来。这时最需要的是轻快的 **跳转与引用**，而不是再背一整套 IDE。

VS Code 里常见的 Java 跳转往往要 **复杂配置**（JDK、语言服务、项目导入……），体感却仍不稳定。于是有了 **开箱即用** 的 Defhop：装上就能导航，专注读代码这一件事。

想要的能力，欢迎到 GitHub **许愿 / 提 Issue**：  
→ [github.com/iCodZilla/defhop](https://github.com/iCodZilla/defhop/issues)

## 请我喝杯咖啡

| 支付宝 | 微信 |
|:---:|:---:|
| <img src="https://raw.githubusercontent.com/iCodZilla/defhop/main/media/alipay.jpg" alt="支付宝收款码" width="220" /> | <img src="https://raw.githubusercontent.com/iCodZilla/defhop/main/media/wechat.jpg" alt="微信收款码" width="220" /> |

## 性能实测（Go 引擎）

本机 Windows 实测（2026-10-27）。解析与导航均在 **Go 后端**完成；下表只按**规模**列举（多模块 Maven 工程），不含项目名称。

| 规模 | 文件 / 模块 | 冷索引 | 跳转定义 DEF | 工作集内存 | 引擎体积 |
|------|-------------|--------|--------------|------------|----------|
| 小型 | **~20** 个 `.java` | **~13 ms** | p95 **~1 ms** | **~14 MB** | **5.66 MB** |
| 中型 | **~570** 个 `.java` · **~10** 个一级模块（约 **14** 个 Maven 模块） | **~0.3 s** | p95 **~2 ms** | **~70 MB** | 同上 |
| 大型 | **~9,000** 个 `.java` · **~10** 个一级模块（约 **60** 个 Maven 模块）· ~10 万 symbols | **~9 s** | p95 **~1 ms** | **~830 MB** | 同上 |

读法：中型仓亚秒级就绪；近万文件级约数秒完成索引；跳转保持毫秒级；内存是导航引擎占用，不是再挂一整套 JVM Language Server。

## 为什么选 Defhop

| 你在意的 | Defhop |
|----------|--------|
| **Go 解析** | 原生编译、多核吃满，不为导航再拉起 JVM |
| **占用低** | 单引擎约数 MB～百 MB 级，见上表实测 |
| **效率高** | 冷索引 / 热更新 / `F12` 均为毫秒～百毫秒级 |
| **够用就好** | 只做导航，不和 AI 补全抢活 |
| **框架懂行** | Spring、MyBatis、Feign 等按需启用 |

## 支持能力

下列为导航能力（转到定义 / 查找引用等）。✅ = 已支持。框架类插件**检测到再用**，未用到的不占资源。

### Java 语言 / 语法

| 能力 | 状态 |
|------|:----:|
| 类 / 接口 / 枚举 → 定义 | ✅ |
| 方法 / 构造 / 字段 → 定义 | ✅ |
| 继承与实现（子类 / 接口实现） | ✅ |
| 方法重载 | ✅ |
| 泛型类型参数（如 `Box<T>`） | ✅ |
| 查找引用（含声明） | ✅ |
| 查找实现 | ✅ |
| Lambda 表达式内局部变量 | ✅ |
| 方法引用（`Type::method` / `Type::getXxx`） | ✅ |
| Stream / Optional 链式中的跳转 | ✅ |
| 包内跨文件类型解析 | ✅ |

### Spring

| 能力 | 状态 |
|------|:----:|
| `@Service` / `@Component` / `@RestController` 类型跳转 | ✅ |
| 注入字段 / 构造注入调用 → 实现 | ✅ |
| `@Value("${…}")` ↔ `application.yml` / `.properties` | ✅ |
| 嵌套占位符（如 `${spring.application.name}`） | ✅ |
| `@ConfigurationProperties` 字段 ↔ 配置键 | ✅ |
| 配置文件键 → Java `@Value` / 属性字段 | ✅ |

### Spring Cloud

| 能力 | 状态 |
|------|:----:|
| OpenFeign `@FeignClient` 方法 → Controller | ✅ |
| Controller 方法 → Feign 引用 | ✅ |
| 路径变量风格映射（如 `/items/{id}/…`） | ✅ |
| `@EnableFeignClients` 工程识别 | ✅ |

### MyBatis

| 能力 | 状态 |
|------|:----:|
| Mapper 接口方法 → XML `<select\|insert\|…>` | ✅ |
| XML statement → Java Mapper 声明 | ✅ |
| 业务代码中调用 Mapper 方法跳转 | ✅ |

### MyBatis-Plus

| 能力 | 状态 |
|------|:----:|
| `ServiceImpl` 中 `baseMapper` → 具体 Mapper 类型 | ✅ |
| `baseMapper.xxx()` → Mapper 自定义方法 | ✅ |
| `BaseMapper` / `IService` 工程检测 | ✅ |

### Lombok

| 能力 | 状态 |
|------|:----:|
| `@Data` / `@Getter` / `@Setter` 生成访问器可导航 | ✅ |
| `@Builder` 相关成员导航 | ✅ |
| 方法引用中的 Lombok getter（如 `Customer::getName`） | ✅ |

### MapStruct

| 能力 | 状态 |
|------|:----:|
| `@Mapper` 接口方法 → 生成/手写 Impl | ✅ |
| `@Mapping(target = "…")` → DTO / 实体字段 | ✅ |

> 仓库内 `testdata/samples` 覆盖上述能力的冒烟用例（含 lambda）。

## 怎么用

1. 从扩展市场安装 **Defhop for Java**，重新加载窗口  
2. 打开你的 **Maven / Gradle Java 项目**  
3. 看右下角索引进度，完成后即可导航  
4. 光标放在符号上 → **`F12` 跳转定义**，或右键 **查找引用**

常用命令（命令面板搜 `Defhop`）：

- **Reindex Workspace** — 重新索引  
- **Go to MyBatis SQL** — 从 Java 跳到 Mapper XML  
- **Go to Java Declaration** — 从 XML 跳回 Java  

## 和「大型 Java 插件」的区别

| | 传统 Java 套件 | Defhop |
|--|----------------|--------|
| 定位 | 补全 + 重构 + 调试 + … | **Java 导航助手** |
| 引擎 | 常依赖 JVM Language Server | **Go 原生解析**，无 JVM |
| 体感 | 功能全，往往更重 | 功能少，专注速度 |
| AI 时代 | 大量能力和 AI 重叠 | **互补**：AI 负责写，Defhop 负责读与跳 |

---

# English

## What it is

**Defhop** is a **Java navigation helper**: Go to Definition, Find References, implementations, and smart jumps across Spring / MyBatis / Feign / Lombok / MapStruct.

The indexer and navigation engine are written in **Go**—native multi-core parsing, **no JVM** required to run the nav backend. Small binary, low RAM, fast hops.

Built for **reading code** in large repos—especially when AI already writes the code and you just need to hop around fast.

> No completion. No refactoring. No full IDE suite. Just navigation—fast and lean.

## Why it exists

In the AI era, people write far less code by hand—assistants draft, rewrite, and patch. Features that once felt essential—**completion and refactoring**—often become dead weight: heavy, resource-hungry, and largely overlapping with AI.

You still need to **read code** sometimes: follow a call, check an implementation, see where logic comes from. What you want then is snappy **go-to-definition / references**, not another full IDE stacked on VS Code.

Stock Java navigation in VS Code usually means **painful setup** (JDK, language server, project import…) with uneven results. **Defhop** is the out-of-the-box alternative: install and navigate—built only for reading code.

Wish-list welcome on GitHub:  
→ [github.com/iCodZilla/defhop](https://github.com/iCodZilla/defhop/issues)

## Buy me a coffee

| Alipay | WeChat Pay |
|:---:|:---:|
| <img src="https://raw.githubusercontent.com/iCodZilla/defhop/main/media/alipay.jpg" alt="Alipay QR" width="220" /> | <img src="https://raw.githubusercontent.com/iCodZilla/defhop/main/media/wechat.jpg" alt="WeChat QR" width="220" /> |

## Benchmarks (Go engine)

Measured on Windows (2026-10-27). Parsing & navigation run in the **Go backend**. Rows are by **scale only** (multi-module Maven workspaces)—no project names.

| Scale | Files / modules | Cold index | Go to Definition | Working set | Engine binary |
|-------|-----------------|------------|------------------|-------------|---------------|
| Small | **~20** `.java` | **~13 ms** | p95 **~1 ms** | **~14 MB** | **5.66 MB** |
| Medium | **~570** `.java` · **~10** top-level modules (~**14** Maven modules) | **~0.3 s** | p95 **~2 ms** | **~70 MB** | same |
| Large | **~9,000** `.java` · **~10** top-level modules (~**60** Maven modules) · ~100k symbols | **~9 s** | p95 **~1 ms** | **~830 MB** | same |

Takeaway: medium repos are ready in under a second; ~10k-file workspaces index in a few seconds; jumps stay around **1–2 ms**; RAM is the nav engine—not a second full JVM language server.

## Why Defhop

| You care about | Defhop |
|----------------|--------|
| **Go parser** | Native, multi-core—no JVM just to navigate |
| **Low footprint** | Single-digit to low-hundreds MB—see table |
| **High throughput** | Cold / warm / `F12` in ms–hundreds of ms |
| **Stay lean** | Navigation only—won't fight your AI assistant |
| **Frameworks** | Spring, MyBatis, Feign… on demand |

## Capabilities

Navigation features below (Go to Definition / Find References, etc.). ✅ = supported. Framework plugins load **on demand**.

### Java language / syntax

| Feature | |
|---------|:-:|
| Class / interface / enum → definition | ✅ |
| Method / constructor / field → definition | ✅ |
| Inheritance & implementations | ✅ |
| Method overloads | ✅ |
| Generics (e.g. `Box<T>`) | ✅ |
| Find references (incl. declaration) | ✅ |
| Find implementations | ✅ |
| Locals inside lambdas | ✅ |
| Method references (`Type::method` / `Type::getXxx`) | ✅ |
| Jumps inside Stream / Optional chains | ✅ |
| Cross-file types in the package | ✅ |

### Spring

| Feature | |
|---------|:-:|
| `@Service` / `@Component` / `@RestController` type jumps | ✅ |
| Injected field / ctor calls → implementation | ✅ |
| `@Value("${…}")` ↔ `application.yml` / `.properties` | ✅ |
| Nested placeholders (e.g. `${spring.application.name}`) | ✅ |
| `@ConfigurationProperties` field ↔ config key | ✅ |
| Config key → Java `@Value` / property field | ✅ |

### Spring Cloud

| Feature | |
|---------|:-:|
| OpenFeign `@FeignClient` method → Controller | ✅ |
| Controller method → Feign references | ✅ |
| Path-variable style mappings | ✅ |
| `@EnableFeignClients` project detection | ✅ |

### MyBatis

| Feature | |
|---------|:-:|
| Mapper method → XML `<select\|insert\|…>` | ✅ |
| XML statement → Java Mapper | ✅ |
| Calls to Mapper methods in business code | ✅ |

### MyBatis-Plus

| Feature | |
|---------|:-:|
| `baseMapper` in `ServiceImpl` → concrete Mapper | ✅ |
| `baseMapper.xxx()` → custom Mapper method | ✅ |
| `BaseMapper` / `IService` detection | ✅ |

### Lombok

| Feature | |
|---------|:-:|
| `@Data` / `@Getter` / `@Setter` generated accessors | ✅ |
| `@Builder` member navigation | ✅ |
| Lombok getters in method refs (e.g. `Customer::getName`) | ✅ |

### MapStruct

| Feature | |
|---------|:-:|
| `@Mapper` method → generated / hand-written Impl | ✅ |
| `@Mapping(target = "…")` → DTO / entity field | ✅ |

> See `testdata/samples` for smoke coverage (including lambdas).

## How to use

1. Install **Defhop for Java** and reload  
2. Open a **Maven / Gradle** Java workspace  
3. Wait for the status-bar index  
4. **`F12`** / Find References  

Command Palette → search `Defhop` for reindex and MyBatis ↔ Java routes.

## vs full Java packs

| | Classic Java packs | Defhop |
|--|--------------------|--------|
| Role | Full IDE features | **Java navigation helper** |
| Engine | Often a JVM language server | **Go-native parser**, no JVM |
| Feel | Complete, often heavy | Focused, fast |
| AI era | Overlaps assistants | **Complement**: AI writes, Defhop jumps |

---

<p align="center">
  <img src="https://raw.githubusercontent.com/iCodZilla/defhop/main/media/icon.png" alt="Defhop" width="64" /><br/>
  <sub>Defhop for Java · Java 导航 · 跳转定义 · 查找引用</sub><br/>
  <sub><a href="https://github.com/iCodZilla/defhop/issues">GitHub 许愿 / Feature requests</a></sub>
</p>
