//! **退出 / 注销的确认框**（架构方向 ①）。
//!
//! ⚠️ 不碰 SDL：只有"要确认的是哪个动作"，于是"确认框开着时该不该吞掉别的输入"
//! 这类规则能进测试。
//!
//! 为什么要它：**删除/退出这类不可恢复的动作，必须要求一个明确的"是"**
//! —— 原版 `DMessageDlg` 就是这个意思（回车/[确定] 才算，其余输入 = 取消）。

/// 待确认的动作。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum QuitAct {
    /// 注销：断线回登录界面（原版 `AppLogOut`）。
    Logout,
    /// 退出：关掉客户端（原版 `AppExit`）。
    Exit,
}

impl QuitAct {
    /// 确认框上那句话。
    pub(crate) fn prompt(self) -> &'static str {
        match self {
            QuitAct::Logout => "确定要注销吗？",
            QuitAct::Exit => "确定要退出游戏吗？",
        }
    }
}

/// 确认框（模态）：`None` = 没开着。
#[derive(Debug, Clone, Copy, Default)]
pub(crate) struct Confirm {
    act: Option<QuitAct>,
}

impl Confirm {
    pub(crate) fn new() -> Self {
        Self { act: None }
    }

    /// 开着一个确认框吗？
    pub(crate) fn is_open(self) -> bool {
        self.act.is_some()
    }

    /// 弹出确认框（问要不要做 `act`）。
    pub(crate) fn ask(&mut self, act: QuitAct) {
        self.act = Some(act);
    }

    /// 取消（Esc / 点 [取消] / 点别处）。
    pub(crate) fn cancel(&mut self) {
        self.act = None;
    }

    /// **当前待确认的动作**（只读，画确认框用；不消费）。
    pub(crate) fn pending(self) -> Option<QuitAct> {
        self.act
    }

    /// 那句话（`None` = 没开 ⇒ 没什么可画）。
    #[allow(dead_code)] // 目前 `main` 直接用 `QuitAct::prompt`；留着是给"只问不问是什么"的场合
    pub(crate) fn prompt(self) -> Option<&'static str> {
        self.act.map(QuitAct::prompt)
    }

    /// **确认**：取出那个动作并关掉框（`None` = 本来就没开 ⇒ 什么都不做）。
    pub(crate) fn confirm(&mut self) -> Option<QuitAct> {
        self.act.take()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 没开时确认什么都不做() {
        let mut c = Confirm::new();
        assert!(!c.is_open());
        assert_eq!(c.confirm(), None);
        assert_eq!(c.prompt(), None);
    }

    #[test]
    fn 问了才有得确认() {
        let mut c = Confirm::new();
        c.ask(QuitAct::Exit);
        assert!(c.is_open());
        assert_eq!(c.prompt(), Some("确定要退出游戏吗？"));
        assert_eq!(c.confirm(), Some(QuitAct::Exit));
        assert!(!c.is_open(), "确认完框就关了");
    }

    #[test]
    fn 取消后不能再确认() {
        let mut c = Confirm::new();
        c.ask(QuitAct::Logout);
        c.cancel();
        assert!(!c.is_open());
        assert_eq!(c.confirm(), None, "取消了就不该还能确认出去");
    }

    #[test]
    fn 重复问以最后一次为准() {
        let mut c = Confirm::new();
        c.ask(QuitAct::Logout);
        c.ask(QuitAct::Exit);
        assert_eq!(c.confirm(), Some(QuitAct::Exit));
    }

    #[test]
    fn 注销与退出的问法不同() {
        assert_ne!(QuitAct::Logout.prompt(), QuitAct::Exit.prompt());
    }
}
