用go写个服务器，作为HTTP代理/SOCKS代理，或者接受将目标URL作为path的HTTP服务器形如`http://mitmcdn.example.com:8081/https://origin.cdn.com/file.exe`，通过中间人攻击与SNI检测技术，把发往CDN域名的HTTP/HTTPS流量拦截，自己请求目标文件并且完整缓存文件（即使原始的客户端目前还不需要完整的文件内容）。当客户端请求下载某个文件时，发送已有的片段并优先下载该文件的剩余部分（即把其他文件的下载延后）。

使用config.toml指定：
- 需要应用拦截的CDN的域名，以及特定处理规则（比如：只用url path最后的文件名部分来判断是否为同一文件）
- 单个文件大小限制
- 缓存路径
- 缓存文件总大小限制
- 缓存文件时长
- 上级代理（http / socks5）

使用 mitmcdn.db 这一sqlite3数据库存储：
- log
- 文件的metadata，比如请求的URL，使用的cookie，文件名，文件大小，下载时间，保存路径

这是为了在不稳定网络条件下主动缓存视频等内容，并且在需要时轻松覆盖原内容。