//! 世界聊天输入行（原版 `PlayScn` 的 `m_EditSayMsg`，回车打开、回车发送）。
//!
//! **纯状态**：只记"正在打的这行字"，不碰 SDL、不发包 ⇒ 打开/退格/提交换行都能单测
//!（与 `qty` / `bagdrag` 同一条纪律）。
//!
//! 频道不在这里分：原版是**文本前缀**（`!` 喊话、`!!` 组队、`/名字` 私聊），
//! 服务端 `handleChat` 认同一套前缀 ⇒ 客户端只管把整行原样发出去。

/// 一行最多多少**字符**（原版 `SayMsgMaxLen` 是字节口径；我们按字符算，
/// 免得一个汉字被劈掉半个）。
pub(crate) const MAX_CHARS: usize = 80;

/// 聊天输入行的状态。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct ChatInput {
    text: String,
}

impl ChatInput {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    pub(crate) fn text(&self) -> &str {
        &self.text
    }

    pub(crate) fn is_empty(&self) -> bool {
        self.text.is_empty()
    }

    /// 追加一段输入（SDL 的 `TextInput` 一次给整段，含中文/粘贴）。
    ///
    /// 超上限的尾巴直接丢；换行符忽略（Enter 是发送键，不该进正文）。
    pub(crate) fn push_str(&mut self, s: &str) {
        let mut have = self.text.chars().count();
        for ch in s.chars() {
            if have >= MAX_CHARS {
                break;
            }
            if ch == '\n' || ch == '\r' {
                continue;
            }
            self.text.push(ch);
            have += 1;
        }
    }

    /// 退格删**一个字符**（`String::pop` 就是按字符，不会劈开 UTF-8）。
    pub(crate) fn backspace(&mut self) {
        self.text.pop();
    }

    /// 取走这行字（发送后状态清空）。
    pub(crate) fn take(&mut self) -> String {
        std::mem::take(&mut self.text)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 追加中文按字符算且忽略换行() {
        let mut c = ChatInput::new();
        c.push_str("你好\n世界");
        assert_eq!(c.text(), "你好世界");
        assert!(!c.is_empty());
    }

    #[test]
    fn 超上限的部分直接丢掉() {
        let mut c = ChatInput::new();
        c.push_str(&"字".repeat(MAX_CHARS));
        c.push_str("多余的");
        assert_eq!(c.text().chars().count(), MAX_CHARS);
    }

    #[test]
    fn 退格一次删一个汉字() {
        let mut c = ChatInput::new();
        c.push_str("你好a");
        c.backspace();
        c.backspace();
        assert_eq!(c.text(), "你");
        c.backspace();
        assert!(c.is_empty());
        c.backspace(); // 空的时候再退格不 panic
        assert_eq!(c.text(), "");
    }

    #[test]
    fn 取走之后状态是空的() {
        let mut c = ChatInput::new();
        c.push_str("@atkmode");
        assert_eq!(c.take(), "@atkmode");
        assert!(c.is_empty());
    }
}
