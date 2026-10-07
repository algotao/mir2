#!/usr/bin/env python3
"""从原版 Delphi 源码抽取 actor 的**动作表**，生成 `core/src/actor.rs` 里的常量段。

⚠️ 为什么是生成而不是手抄：人物 14 个动作 + 怪物 38 张表 × 7 个动作 × 4 个字段，
手抄必错，而且错了以后"某一帧的图号差一格"这种问题极难发现（看上去只是动作怪）。
生成器把"数值来自哪里"变成可复现的：源文件 + 行号都在下面的常量里。

用法（源码不在本仓库里，需手动指向）：

    python3 client/core/tools/gen_actor_tables.py \\
        /Users/taohuifeng/Develop/git/mir2standard/GameOfMir/Client/Actor.pas

输出到 stdout；把它贴进 `client/core/src/actor.rs` 的"生成段"（该文件里有标记）。
"""
import json
import pathlib
import re
import sys

# 服务端的怪物表：用来算"哪些品种真的要带表"与"兜底选哪张"。
# ⚠️ 手抄那串品种号必然与数据漂移 —— 这里读真源。
REPO = pathlib.Path(__file__).resolve().parents[3]
monsters_json = REPO / "server/data/monsters.json"

# ---------- Pascal 预处理 ----------

def strip_comments(src: str) -> str:
    """去掉 `{ ... }` 与 `(* ... *)`（含跨行）。

    ⚠️ `9{01}: Result:=@MA9;` 里的 `{01}` **本身就是 Pascal 注释**（原版用它标编号），
    去注释之后正好得到干净的 `9: Result:=@MA9;`。
    """
    src = re.sub(r"\{.*?\}", "", src, flags=re.S)
    src = re.sub(r"\(\*.*?\*\)", "", src, flags=re.S)
    # `//` 行注释同样要去：原版常把作废的备用值留在注释里
    #（如 `21` 那一支里 `6: Result := 2260;` 已被注释，真正生效的是下一行 2440），
    # 留着会生成重复的 match 分支。
    return re.sub(r"//[^\n]*", "", src)


def parse_labels(text: str) -> list:
    """`2,3,7..12` → [2,3,7,8,9,10,11,12]（Pascal 的区间标签）。"""
    out = []
    for part in text.split(","):
        part = part.strip()
        if ".." in part:
            a, b = part.split("..")
            out.extend(range(int(a), int(b) + 1))
        elif part:
            out.append(int(part))
    return out


def find_case_block(src: str, head) -> "re.Match":
    """从一个 `case X of` 的开头，配平 `case/begin/end` 找到它真正的结尾。

    返回的 match 的 group(1) 是 `case ... of` 与结尾 `end` **之间**的正文。
    """
    depth = 1
    end = len(src)
    for m in re.finditer(r"\b(case|begin|end)\b", src[head.end():]):
        depth += 1 if m.group(1) in ("case", "begin") else -1
        if depth == 0:
            end = head.end() + m.start()
            break
    return re.match(r"(?s)(.*)", src[head.end():end])


def parse_actions(body: str) -> dict:
    """解析 `A:(Start:0; frame:1; skip:7; ftime:200; usetick:0)` 形式的一串动作。"""
    out = {}
    for m in re.finditer(
        r"(\w+)\s*:\s*\(\s*start\s*:\s*(\d+)\s*;\s*frame\s*:\s*(\d+)\s*;\s*skip\s*:\s*(\d+)\s*;\s*ftime\s*:\s*(\d+)",
        body,
        re.I,  # Pascal 不区分大小写：源里是 `start`，别写死成 `Start`
    ):
        out[m.group(1)] = tuple(int(m.group(i)) for i in range(2, 6))
    return out


# ---------- 抽取 ----------

def main(path: str) -> None:
    src = strip_comments(open(path, errors="replace").read())

    # 人物：HA: THumanAction = ( ... );
    ha_m = re.search(r"HA\s*:\s*THumanAction\s*=\s*\((.*?)\n\s*\);", src, re.S)
    ha = parse_actions(ha_m.group(1))

    # 怪物：MA<n>: TMonsterAction = ( ... );
    mas = {}
    for m in re.finditer(r"(MA\d+)\s*:\s*TMonsterAction\s*=\s*\((.*?)\n\s*\);", src, re.S):
        mas[m.group(1)] = parse_actions(m.group(2))

    # 映射：取**最后**一个 `case Race of`（前面那个是被注释掉的旧表）。
    # ⚠️ 结束位置必须用 `case/begin/end` 配平来定：`Race=50` 那支里面还嵌了一层
    # `case Appr of ... end;`，用"到第一个 end 为止"的非贪婪匹配会在那层就收尾，
    # 把后半张表（52..99）**整段丢掉** —— 而且丢得毫无迹象（match 只是少几条臂）。
    blocks = [find_case_block(src, m) for m in re.finditer(r"case\s+Race\s+of", src)]
    # ⚠️ 必须按**缩进**过滤：`Race=50` 那一支里面还嵌了一层 `case Appr of`，
    # 内层的标签凭空多出来（`23 -> MA36`），会把外层的真值（`23 -> MA14`）覆盖掉。
    rows = [(len(m.group(1)), m.group(2), m.group(3)) for m in re.finditer(
        r"^( *)([\d\.]+(?:\s*,\s*[\d\.]+)*)\s*:\s*Result\s*:=\s*@(MA\d+)", blocks[-1].group(1), re.M)]
    top = min(ind for ind, _, _ in rows)
    mapping = {}
    for ind, labs, tbl in rows:
        if ind != top:
            continue
        for lab in parse_labels(labs):
            mapping[lab] = tbl
    # Race=50 那支按 Appr 再分（原版嵌套 case），我们的数据里没有 ⇒ 单独标出来
    nested = "case Appr of" in blocks[-1].group(1)

    # GetOffset
    # ⚠️ 文件里有两处 `function GetOffset (...)`：接口段的**前置声明**与真正的实现。
    # 取"后面跟着 `nrace` 那句"的那个。
    off_m = None
    for m in re.finditer(r"function\s+GetOffset\s*\(appr:\s*integer\)\s*:\s*integer;(.*?)\nend;", src, re.S):
        if "nrace" in m.group(1)[:400]:
            off_m = m
    body = off_m.group(1)

    # 分支按**缩进**分层：外层 `case nrace of` 的标签缩进最浅，内层 `case npos of` 更深。
    labels_found = [(m.start(), m.end(), len(m.group(1)), m.group(2)) for m in
                    re.finditer(r"^([ \t]*)([\d\.]+(?:\s*,\s*[\d\.]+)*)\s*:\s*", body, re.M)]
    top = min(ind for _, _, ind, _ in labels_found)
    cuts = [(start, end, labs) for start, end, ind, labs in labels_found if ind == top]
    branches = []
    for i, (start, after_colon, labs) in enumerate(cuts):
        end = cuts[i + 1][0] if i + 1 < len(cuts) else len(body)
        branches.append((parse_labels(labs), body[after_colon:end]))

    print("// ---------- 以下为生成段（勿手改，见 client/core/tools/gen_actor_tables.py）----------")
    print(f"// 源：{path}")
    print()

    # 人物动作表
    print("/// 人物动作表（原版 `HA: THumanAction`，Actor.pas:75-91）。")
    print("///")
    print("/// 顺序即 `HAct` 的判别式顺序，**不能重排**。")
    print("pub const HA: [Act; 14] = [")
    order = [
        "ActStand", "ActWalk", "ActRun", "ActRushLeft", "ActRushRight", "ActWarMode",
        "ActHit", "ActHeavyHit", "ActBigHit", "ActFireHitReady", "ActSpell",
        "ActSitdown", "ActStruck", "ActDie",
    ]
    for name in order:
        s, f, k, t = ha[name]
        print(f"    Act {{ start: {s}, frame: {f}, skip: {k}, ftime: {t} }}, // {name}")
    print("];")
    print()

    # 怪物动作表（去重）
    uniq = {}
    for name, acts in mas.items():
        key = tuple(sorted(acts.items()))
        uniq.setdefault(key, []).append(name)
    print("/// 怪物动作表（原版 `MA<n>: TMonsterAction`，Actor.pas:92-847），已去重。")
    print("///")
    print("/// ⚠️ 只生成了**我们的数据真正用到**的品种 + 出现次数最多的那张（当兜底）；")
    print("/// 原版对没列出的品种是 `Result := nil`（就是崩溃），我们不学它。")
    # ⚠️ 品种清单**从数据算**，不手抄：只有服务端真会下发的 RaceImg 才值得带表
    used_race_img = {r["race_img"] for r in json.loads(monsters_json.read_text())} if monsters_json.exists() else set(mapping)
    # 兜底表：按**我们数据的实际覆盖**选（哪个表覆盖的怪最多）。
    # 原版对没列出的品种是 `Result := nil`（即崩溃），不能学；
    # 而"覆盖最多"的那个表至少让大多数怪的动作是对的。
    usage = {}
    if monsters_json.exists():
        for r in json.loads(monsters_json.read_text()):
            tbl = mapping.get(r["race_img"])
            if tbl:
                usage[tbl] = usage.get(tbl, 0) + 1
    if not usage:
        usage = {max(uniq.items(), key=lambda kv: len(kv[1]))[1][0]: 1}
    fallback = max(usage.items(), key=lambda kv: kv[1])[0]
    covered = sum(usage.values())
    total = sum(1 for r in json.loads(monsters_json.read_text())) if monsters_json.exists() else 0
    need = sorted({mapping[r] for r in used_race_img if r in mapping} | {fallback})
    for name in need:
        s = mas[name]
        sib = next(v for k, v in uniq.items() if name in v)
        print(f"/// 原版 `{name}`（内容相同的还有 {len(sib) - 1} 张：" +
              f"{', '.join(sorted(set(sib) - {name}, key=lambda x: int(x[2:]))[:6])}…）")
        print(f"pub const {name.upper()}: [Act; 7] = [")
        for a in ["ActStand", "ActWalk", "ActAttack", "ActCritical", "ActStruck", "ActDie", "ActDeath"]:
            st, f, k, t = s[a]
            print(f"    Act {{ start: {st}, frame: {f}, skip: {k}, ftime: {t} }}, // {a}")
        print("];")
        print()

    # 映射
    print("/// `RaceImg` → 动作表（原版 `GetRaceByPM`，Actor.pas:848-954）。")
    print("///")
    print("/// ⚠️ 参数名在原版里叫 `Race`，但喂进去的是 **`RACEfeature(c_feature)`**（低字节）")
    print("/// = 我们协议里的 `RaceImg` —— 不是服务端的 `Race` 字段（那个只用于 AI，不下发）。")
    print(f"///")
    print(f"/// ⚠️ 原版 `Race=50` 那一支还有一层 `case Appr of`（嵌套={nested}）：我们的数据没有")
    print("/// `RaceImg=50`，**故意没生成**；要支持时照 Actor.pas:881-915 补。")
    print("pub fn mon_actions(race_img: u8) -> &'static [Act; 7] {")
    print("    match race_img {")
    by_table = {}
    for race, tbl in sorted(mapping.items()):
        if tbl in need:
            by_table.setdefault(tbl, []).append(race)
    for tbl, races in sorted(by_table.items(), key=lambda kv: min(kv[1])):
        labels = " | ".join(str(r) for r in races)
        print(f"        {labels} => &{tbl.upper()},")
    print(f"        // 原版对没列出的品种是 `Result := nil` ⇒ 崩溃；我们退回 {fallback}")
    print(f"        // （挑它是因为它覆盖我们数据里最多的怪：{usage[fallback]}/{total} 只；"
          f"能对上表的共 {covered}/{total} 只）")
    print(f"        _ => &{fallback.upper()},")
    print("    }")
    print("}")
    print()

    # GetOffset
    print("/// 怪物外观号 → 图片块起点（原版 `GetOffset`，Actor.pas:1003-1218）。")
    print("///")
    print("/// `nrace = Appr / 10` 选“哪一段”，`npos = Appr % 10` 选段内第几个。")
    print("pub fn mon_offset(appr: u16) -> u32 {")
    print("    if appr >= 1000 {")
    print("        return 0; // 原版：>=1000 走外部 `Graphics\\Monster\\<Appr>.wil`，偏移从 0 算")
    print("    }")
    print("    let nrace = appr / 10;")
    print("    let npos = (appr % 10) as u32;")
    print("    match nrace {")
    for labels, code in branches:
        code = code.strip()
        labs = " | ".join(str(l) for l in labels)
        simple = re.fullmatch(r"Result\s*:=\s*npos\s*\*\s*(\d+)\s*;", code)
        if simple:
            print(f"        {labs} => npos * {simple.group(1)},")
            continue
        # 4 那种：先给公式，再特判
        if "if npos = 1" in code:
            base = re.search(r"Result\s*:=\s*npos\s*\*\s*(\d+)", code).group(1)
            print(f"        {labs} => if npos == 1 {{ 600 }} else {{ npos * {base} }},")
            continue
        inner = re.findall(r"(\d+)\s*:\s*Result\s*:=\s*(\d+)\s*;", code)
        default = re.search(r"else\s+Result\s*:=\s*npos\s*\*\s*(\d+)", code)
        if inner:
            arms = " ".join(f"{n} => {v}," for n, v in inner)
            tail = f" _ => npos * {default.group(1)}," if default else " _ => 0,"
            print(f"        {labs} => match npos {{ {arms}{tail} }},")
            continue
        print(f"        // !! 没解析出来的分支 {labs}: {code[:60]!r}")
    print("        _ => 0,")
    print("    }")
    print("}")
    print("// ---------- 生成段结束 ----------")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
