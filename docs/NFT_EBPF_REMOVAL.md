# NFT / eBPF 移除说明

自 **2026-03-10** 起，项目已移除以下能力：

- `nft_add` 插件
- `nft_ip` 列表与相关 Web UI 入口
- `nft_add` 内的 eBPF 相关实现与依赖

## 影响范围

1. `config/sub_config/rule_set.yaml` 不再包含 `nft_add` 与 `nft_ip`
2. Web UI 不再显示：
   - NFT 分流规则类型
   - `NFT IP` 列表管理入口
3. 启动行为变更（fail-fast）：
   - 若配置中仍出现 `type: nft_add` / `type: nft_ip` / `type: ebpf`，启动会直接报错并中止。
   - 错误信息会指向本迁移文档，便于快速定位并清理旧配置。

## 迁移建议

- 如果你仍保留旧版配置中的 `nft_add`、`nft_ip`，请删除这些配置项
- 如果外部脚本仍依赖 NFT 相关页面入口，请同步调整
- 现有 DNS 分流、缓存、重写、requery 等功能不受影响
