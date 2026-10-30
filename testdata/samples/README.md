# Defhop samples

Java navigation fixtures used by `scripts/smoke-test-samples.js`.

**Policy:** every user-facing nav feature must have a sample here **and** a smoke assertion. Keep this table, the fixtures, `scripts/smoke-test-samples.js`, and root `README.md` capabilities in sync (see `.cursor/rules/feature-samples-smoke.mdc`).

Covers:

| Area | What to try |
|------|-------------|
| **lang** | classes, interfaces, generics, overloads, enums, records, lambda, method refs |
| **deps** | F12 into `.defhop-deps/*-sources.jar` |
| **spring** | `@Service` / `@RestController` / `@Value` / `@ConfigurationProperties` |
| **spring** | `@Qualifier("primaryPayment")` → `PrimaryPaymentGateway` |
| **spring** | `application-dev.yml` profile keys (`samples.dev.banner`) |
| **spring** | same-path Controller peer (`OrderController` ↔ `OrderCompatController`) |
| **cloud** | OpenFeign ↔ Controller |
| **mybatis** | Mapper ↔ XML; `@Select` annotation Mapper (Go to MyBatis SQL) |
| **mybatisplus** | `ServiceImpl` / `baseMapper` |
| **lombokdemo** | `@Data` / `@Builder`; `@NoArgs/@Required/@AllArgsConstructor` / `@SuperBuilder` |
| **mapstructdemo** | `@Mapper` / `@Mapping` / Impl |
| **gradle/** | `libs.versions.toml` Version Catalog (dependency discovery) |

Also exercised by smoke: **Document Highlight** on Lombok accessors.

Run:

```powershell
node .\scripts\smoke-test-samples.js
```
