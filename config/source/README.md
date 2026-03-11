# Config Source Layout

`config/` 目录下的 YAML 仍然是当前运行时直接加载的配置产物。

后续的精简重构会把“人工维护的源配置”逐步放到 `config/source/`，再生成当前 `config/config.yaml` 与 `config/sub_config/*.yaml`。

当前约定：

- `config/config.yaml` 与 `config/sub_config/*.yaml`：默认运行产物
- `config/examples/`：不接入默认主链路的示例/实验配置
- `config/source/`：后续用于维护抽象配置与生成规则

在生成链落地前，请继续直接修改运行产物；但新增实验配置不要再放回 `config/sub_config/`。
