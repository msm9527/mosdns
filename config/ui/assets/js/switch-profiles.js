(function () {
  const profiles = [
    {
      tag: 'core_mode',
      name: '核心运行模式',
      desc: '切换未命中域名走兼容 / 安全补判链',
      tip: 'compat=兼容模式，未命中走 leak 链；secure=安全模式，未命中走 noleak 链并保留 ECS 首查回退。',
      control: 'select',
      modes: {
        compat: { name: '兼容模式', icon: 'fa-globe-americas' },
        secure: { name: '安全模式', icon: 'fa-shield-alt' },
      },
    },
    {
      tag: 'block_response',
      name: '结果屏蔽',
      desc: '拦截黑名单和无结果请求',
      tip: 'on=启用，off=关闭。用于黑名单、无 A、无 AAAA 等屏蔽场景。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'block_query_type',
      name: '类型屏蔽',
      desc: '屏蔽 SOA、PTR、HTTPS 等类型',
      tip: 'on=启用，off=关闭。可减少不必要的类型查询。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'main_cache',
      name: '主缓存',
      desc: '控制真实解析主缓存',
      tip: 'on=启用，off=关闭。影响 cache_main。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'branch_cache',
      name: '分支缓存',
      desc: '控制真实解析分支缓存',
      tip: 'on=启用，off=关闭。影响 cache_branch_domestic、cache_branch_foreign、cache_branch_foreign_ecs。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'fakeip_cache',
      name: 'FakeIP 缓存',
      desc: '控制 fakeip 响应缓存',
      tip: 'on=启用，off=关闭。只影响 FakeIP DNS 应答缓存，不影响系统记录“哪些域名走过 FakeIP 路径”的运行记忆列表。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'probe_cache',
      name: '探测缓存',
      desc: '控制节点探测专用缓存',
      tip: 'on=启用，off=关闭。影响 cache_probe。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'ad_block',
      name: '广告屏蔽',
      desc: '启用 AdGuard 在线规则',
      tip: 'on=启用，off=关闭。开启后，广告规则命中会直接拒绝。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'cn_answer_mode',
      name: '国内应答模式',
      desc: '切换国内域名 realip / fakeip',
      tip: 'realip=返回真实 IP，fakeip=返回 FakeIP。当前勾选代表 fakeip。',
      valueForOn: 'fakeip',
      valueForOff: 'realip',
    },
    {
      tag: 'udp_fast_path',
      name: 'UDP 快路径',
      desc: '启用 UDP 极限缓存快路径',
      tip: 'on=启用，off=关闭。只影响 UDP server 的极限快路径。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
    {
      tag: 'client_proxy_mode',
      name: '客户端代理模式',
      desc: '按 client_ip_whitelist.txt / client_ip_blacklist.txt 控制哪些客户端允许走代理',
      tip: 'all=全部客户端都允许代理；blacklist=client_ip_blacklist.txt 内客户端禁止代理；whitelist=只有 client_ip_whitelist.txt 内客户端允许代理。',
      control: 'select',
      modes: {
        all: { name: '全部', icon: 'fa-globe' },
        blacklist: { name: '黑名单', icon: 'fa-user-lock' },
        whitelist: { name: '白名单', icon: 'fa-user-secret' },
      },
    },
    {
      tag: 'block_ipv6',
      name: 'IPv6 屏蔽',
      desc: '屏蔽 AAAA 请求类型',
      tip: 'on=启用，off=关闭。无 IPv6 环境可按需开启。',
      valueForOn: 'on',
      valueForOff: 'off',
    },
  ];

  const icons = {
    block_response: 'fa-ban',
    client_proxy_mode: 'fa-user-cog',
    core_mode: 'fa-globe-americas',
    main_cache: 'fa-database',
    branch_cache: 'fa-history',
    fakeip_cache: 'fa-layer-group',
    probe_cache: 'fa-satellite-dish',
    block_query_type: 'fa-ban',
    block_ipv6: 'fa-ban',
    ad_block: 'fa-shield-alt',
    cn_answer_mode: 'fa-route',
    udp_fast_path: 'fa-bolt',
  };

  window.MOSDNS_SWITCH_PROFILES = Object.freeze(profiles.map(profile => Object.freeze({ ...profile })));
  window.MOSDNS_SWITCH_UI_PROFILES = Object.freeze(
    profiles.map(profile => Object.freeze({
      ...profile,
      icon: icons[profile.tag] || 'fa-toggle-on',
      name: profile.modes ? profile.name : `${profile.name}开关`,
    })),
  );
})();
