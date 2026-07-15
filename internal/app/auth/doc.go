// Package auth 是认证应用服务：验证码、登录、注册、注销，以及 auth key 与 user 的绑定。
// 第一阶段用开发固定验证码，2FA 配置由 account 服务持久化查询。
//
// 输入输出在 RPC 边界使用 iamxvbaba/td/tg 类型，本包内部只用 internal/domain 模型。
package auth
