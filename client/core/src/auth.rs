//! 口令的**证明**（[D-24①](../../../docs/decisions.md) 挑战应答）：HMAC-SHA256 + PBKDF2-HMAC-SHA256。
//!
//! # 协议长什么样
//!
//! ```text
//! 客户端                                    服务端
//!   │  LoginSaltRequest{account}            │
//!   │ ───────────────────────────────────►   │  查账号，回它的盐（盐不是秘密）
//!   │  LoginSalt{salt, iterations, key_len}  │
//!   │ ◄───────────────────────────────────   │
//!   │                                        │
//!   K = PBKDF2(pw, salt, iterations, key_len)      ← 客户端算得出，服务端只有存着的同一个 K
//!   proof = HMAC-SHA256(K, nonce ‖ account)        ← nonce = 握手时的 ServerHello.session_key
//!   │  Login{account, password_hash: hex(proof)}    │
//!   │ ───────────────────────────────────►   │  用**存着的 K** 重算一遍 HMAC 比对
//! ```
//!
//! 三样东西都不落网络：明文口令、`K`（等价口令）、连 `proof` 本身也绑在**这条连接一次性**的
//! nonce 上（嗅到也重放不了）。服务端**始终不知道口令** —— 这正是"存着的派生值能当 HMAC 密钥"
//! 带来的好处。
//!
//! # 为什么自己实现这两个
//!
//! C-7（零运行期依赖）把依赖当成本；而 `sha2` 本来就在树里（`core` 的黄金哈希回归用它）。
//! HMAC（RFC 2104）与 PBKDF2（RFC 8018）都是**短且完全指定**的构造，代价是正确性得自己保证
//! —— 所以它们被**公开测试向量**钉死：HMAC 用 RFC 4231 的 case 1/2，PBKDF2 用
//! `password`/`salt` 那组公开向量。另外还有一条本项目自己的向量（`PROOF_VECTOR`），
//! 与**服务端 Go** 对同一组输入必须给出同一组字节：那是"两端各写一套实现"的对拍墙。

use sha2::{Digest, Sha256};

/// HMAC-SHA256（RFC 2104 + FIPS 180-4）。
///
/// ⚠️ 密钥**长于**分组（64 字节）时先摘要成 32 字节再当密钥 —— 这一步漏掉的话，
/// 长密钥场景会静默算出另一个值（RFC 2104 第 2 节）。
pub fn hmac_sha256(key: &[u8], data: &[u8]) -> [u8; 32] {
    const BLOCK: usize = 64;
    let mut k = [0u8; BLOCK];
    if key.len() > BLOCK {
        k[..32].copy_from_slice(&Sha256::digest(key));
    } else {
        k[..key.len()].copy_from_slice(key);
    }
    let mut ipad = [0x36u8; BLOCK];
    let mut opad = [0x5cu8; BLOCK];
    for i in 0..BLOCK {
        ipad[i] ^= k[i];
        opad[i] ^= k[i];
    }
    let mut inner = Sha256::new();
    inner.update(ipad);
    inner.update(data);
    let ih = inner.finalize();

    let mut outer = Sha256::new();
    outer.update(opad);
    outer.update(ih);
    outer.finalize().into()
}

/// PBKDF2-HMAC-SHA256（RFC 8018 §5.2）：`F(P, S, c, i)` 逐块 XOR。
///
/// `iterations` 是服务端下发的（`LoginSalt.iterations`）—— **不许**在客户端写死，
/// 否则服务端将来调整迭代次数时，所有客户端会一起登不上去。
pub fn pbkdf2_sha256(password: &[u8], salt: &[u8], iterations: u32, key_len: usize) -> Vec<u8> {
    let mut out = Vec::with_capacity(key_len);
    let mut block: u32 = 1;
    while out.len() < key_len {
        let mut first = Vec::with_capacity(salt.len() + 4);
        first.extend_from_slice(salt);
        first.extend_from_slice(&block.to_be_bytes());
        let mut u = hmac_sha256(password, &first);
        let mut t = u;
        for _ in 1..iterations {
            u = hmac_sha256(password, &u);
            for i in 0..32 {
                t[i] ^= u[i];
            }
        }
        out.extend_from_slice(&t);
        block += 1;
    }
    out.truncate(key_len);
    out
}

/// 登录证明：`HMAC-SHA256(verifier, nonce ‖ account)`。
///
/// ⚠️ 拼接顺序与编码是**协议的一部分**（服务端 `authn.ExpectedProof` 拼的是同一个）：
/// `nonce` 在前、`account` 在后、都以原始字节拼（不做 hex/长度前缀）。
pub fn proof(verifier: &[u8], nonce: &[u8], account: &str) -> [u8; 32] {
    let mut data = Vec::with_capacity(nonce.len() + account.len());
    data.extend_from_slice(nonce);
    data.extend_from_slice(account.as_bytes());
    hmac_sha256(verifier, &data)
}

/// 小写十六进制编码（`Login.password_hash` 是字符串字段）。
///
/// 不引 `hex` crate：这函数 8 行，而多一个依赖是要长期付账的（C-7）。
pub fn to_hex(bytes: &[u8]) -> String {
    const D: &[u8; 16] = b"0123456789abcdef";
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push(D[(b >> 4) as usize] as char);
        s.push(D[(b & 0x0f) as usize] as char);
    }
    s
}

/// 十六进制解码；遇到非法字符或奇数长度返回 `None`（**不 panic** —— 输入来自网络）。
pub fn from_hex(s: &str) -> Option<Vec<u8>> {
    let b = s.as_bytes();
    if !b.len().is_multiple_of(2) {
        return None;
    }
    let val = |c: u8| -> Option<u8> {
        match c {
            b'0'..=b'9' => Some(c - b'0'),
            b'a'..=b'f' => Some(c - b'a' + 10),
            b'A'..=b'F' => Some(c - b'A' + 10),
            _ => None,
        }
    };
    let mut out = Vec::with_capacity(b.len() / 2);
    for pair in b.chunks(2) {
        out.push((val(pair[0])? << 4) | val(pair[1])?);
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// HMAC-SHA256 的**公开测试向量**：RFC 4231 的 case 1 与 case 2。
    ///
    /// 这两条是"我实现对了"的唯一凭据 —— 自己实现密码学构造，就必须对着标准向量。
    #[test]
    fn hmac_rfc4231() {
        // case 1: key = 20 个 0x0b，data = "Hi There"
        let key = [0x0bu8; 20];
        assert_eq!(
            to_hex(&hmac_sha256(&key, b"Hi There")),
            "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"
        );
        // case 2: key = "Jefe"
        assert_eq!(
            to_hex(&hmac_sha256(b"Jefe", b"what do ya want for nothing?")),
            "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
        );
    }

    /// 长于分组的密钥要走"先摘要"那一支（RFC 2104 第 2 节）——
    /// 漏掉的话长密钥会静默算出另一个值，而短密钥的用例发现不了。
    #[test]
    fn hmac_long_key_is_digested() {
        let long = [0xaau8; 100];
        let digested = Sha256::digest(long);
        assert_eq!(hmac_sha256(&long, b"x"), hmac_sha256(&digested, b"x"));
    }

    /// PBKDF2-HMAC-SHA256 的公开向量（`password` / `salt` 那组，c = 1 / 2 / 4096）。
    #[test]
    fn pbkdf2_public_vectors() {
        let want = [
            (
                1,
                "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b",
            ),
            (
                2,
                "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43",
            ),
            (
                4096,
                "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a",
            ),
        ];
        for (c, hex) in want {
            let got = pbkdf2_sha256(b"password", b"salt", c, 32);
            assert_eq!(to_hex(&got), hex, "c={c}");
        }
        // 非 32 的派生长（末块要截断）
        assert_eq!(
            to_hex(&pbkdf2_sha256(
                b"passwordPASSWORDpassword",
                b"saltSALTsaltSALTsaltSALTsaltSALTsalt",
                4096,
                40
            )),
            "348c89dbcbd32b2f32d814b8116e84cf2b17347ebc1800181c4e2a1fb8dd53e1c635518c7dac47e9"
        );
    }

    /// **与本项目服务端（Go）对拍**：同一组输入必须给出同一组字节。
    ///
    /// 这四个十六进制串由 Go 的 `crypto/pbkdf2` + `crypto/hmac` 产出，
    /// 服务端 `server/internal/authn/authn_test.go` 里钉的是**同一组**
    /// （那边验"用存着的 K 重算的 HMAC"，这边验"客户端怎么算出 proof"）。
    /// 任何一边改了口径，这条就会红 —— 这是跨语言实现对拍的墙。
    #[test]
    fn proof_vector_matches_server() {
        let salt = from_hex("000102030405060708090a0b0c0d0e0f").unwrap();
        let nonce = from_hex("101112131415161718191a1b1c1d1e1f").unwrap();
        let k = pbkdf2_sha256(b"pw", &salt, 100_000, 32);
        assert_eq!(
            to_hex(&k),
            "fbe79903d759b826e1d54bd9e4f8beb2de5a87ac86037c76328ad485fd894671"
        );
        assert_eq!(
            to_hex(&proof(&k, &nonce, "tester")),
            "07f7a0c926c9ec65e55c6a6ad631e92bd2ab97c0ee61b90317bb25ec75dd9a89"
        );
    }

    /// hex 编解码：往返一致，且**坏输入返回 `None` 而不是 panic**（它来自网络）。
    #[test]
    fn hex_roundtrip_and_rejects() {
        let raw: Vec<u8> = (0..=255u8).collect();
        assert_eq!(from_hex(&to_hex(&raw)).unwrap(), raw);
        assert!(from_hex("abc").is_none(), "奇数长度");
        assert!(from_hex("zz").is_none(), "非法字符");
        assert_eq!(from_hex("A1b2").unwrap(), vec![0xa1, 0xb2], "大写也要认");
    }

    /// 迭代次数的**下限**语义：c=1 就是一次 HMAC（`U1` 不做 XOR 循环）——
    /// 顺手钉住循环边界，免得写成"至少跑一次 XOR"。
    #[test]
    fn pbkdf2_one_iteration_is_single_hmac() {
        let got = pbkdf2_sha256(b"password", b"salt", 1, 32);
        let mut salt1 = b"salt".to_vec();
        salt1.extend_from_slice(&1u32.to_be_bytes());
        assert_eq!(got, hmac_sha256(b"password", &salt1));
    }
}
