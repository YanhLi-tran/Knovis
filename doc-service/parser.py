"""PDF 解析与结构化分块(简历亮点:标题层级切分).

流程:PDF → pymupdf4llm → Markdown(保留表格)→ 按标题层级切分 → 超长滑动窗口二次切。

分块策略:
- 标题层级切分:按 Markdown #/##/### 标题 + 中文编号(一、(一)、1.)识别小节边界
- heading_path 记录标题层级,如 ["三、财务信息","(一)营业收入"],用于段落召回与引用溯源
- 单块超 chunk_size(默认800)时按 chunk_size/overlap 滑动窗口二次切
- 表格(Markdown | ... |)保留为独立块,chunk_type=table
"""
import os
import re
import hashlib
import logging
from typing import List, Dict, Any, Optional, Tuple

import pymupdf4llm

logger = logging.getLogger("doc-service.parser")

# 标题识别正则
# Markdown 标题:# / ## / ###
_MD_HEADING_RE = re.compile(r"^(#{1,6})\s+(.+?)\s*#*\s*$")
# 中文编号标题(行首):一、二、 / （一）(一) / 1. 1、 / 第一章
_CN_NUM_RE = re.compile(
    r"^(第[一二三四五六七八九十百零\d]+[章节篇部分])\s*[、.．]?\s*(.+)$"
)
_CN_PAREN_RE = re.compile(r"^[（(]([一二三四五六七八九十\d]+)[)）]\s*(.+)$")
_CN_SERIAL_RE = re.compile(r"^([一二三四五六七八九十\d]+)[、.．]\s*(.+)$")
_ARABIC_RE = re.compile(r"^(\d{1,2})[、.．]\s*(.+)$")
# Markdown 表格行
_TABLE_ROW_RE = re.compile(r"^\s*\|.*\|\s*$")
_TABLE_SEP_RE = re.compile(r"^\s*\|[\s:|-]+\|\s*$")


def parse_filename(filename: str) -> Optional[Dict[str, Any]]:
    """从文件名解析元数据:股票代码_年份_公司简称_全称.pdf.

    Returns:
        {company_code, company_name, report_year, report_type} 或 None(解析失败)
    """
    name = filename.lower()
    if not name.endswith(".pdf"):
        return None
    base = filename[: -len(".pdf")]
    parts = base.split("_", 3)
    if len(parts) < 4:
        return None
    code, year_str, short, full = parts[0], parts[1], parts[2], parts[3]
    # 股票代码 6 位数字
    if not re.match(r"^\d{6}$", code):
        return None
    # 年份 4 位数字
    if not re.match(r"^\d{4}$", year_str):
        return None
    return {
        "company_code": code,
        "company_name": short,
        "report_year": int(year_str),
        "report_type": "年报",
    }


def _heading_level(line: str) -> Tuple[int, str]:
    """识别标题行,返回 (level, title);非标题返回 (0, "")."""
    m = _MD_HEADING_RE.match(line)
    if m:
        return len(m.group(1)), m.group(2).strip()
    # 中文编号(只识别明显的行首编号,避免误判正文)
    stripped = line.strip()
    if not stripped or len(stripped) > 80:
        return 0, ""
    m = _CN_NUM_RE.match(stripped)
    if m:
        # 第X章 → level 1
        title = m.group(1) + " " + m.group(2).strip()
        return 1, title
    m = _CN_PAREN_RE.match(stripped)
    if m:
        title = "(" + m.group(1) + ")" + m.group(2).strip()
        return 2, title
    m = _CN_SERIAL_RE.match(stripped)
    if m:
        # 一、xxx → level 1
        title = m.group(1) + "、" + m.group(2).strip()
        return 1, title
    m = _ARABIC_RE.match(stripped)
    if m:
        # 1. xxx → level 3(更深层级)
        title = m.group(1) + ". " + m.group(2).strip()
        return 3, title
    return 0, ""


def _is_table_row(line: str) -> bool:
    return bool(_TABLE_ROW_RE.match(line))


def _is_table_sep(line: str) -> bool:
    return bool(_TABLE_SEP_RE.match(line))


def _section_id(heading_path: List[str]) -> str:
    """heading_path → 稳定 hash(段落召回聚合用)."""
    if not heading_path:
        return "root"
    raw = "|".join(heading_path)
    return hashlib.md5(raw.encode("utf-8")).hexdigest()[:16]


def _sliding_window(text: str, size: int, overlap: int) -> List[str]:
    """超长文本按 size/overlap 滑动窗口切分(字符级)."""
    text = text.strip()
    if len(text) <= size:
        return [text] if text else []
    pieces = []
    step = max(1, size - overlap)
    i = 0
    while i < len(text):
        piece = text[i : i + size]
        if piece.strip():
            pieces.append(piece.strip())
        if i + size >= len(text):
            break
        i += step
    return pieces


def _flush_section(
    buf_lines: List[str],
    heading_path: List[str],
    page_num: int,
    chunk_index_start: int,
    chunk_size: int,
    overlap: int,
) -> Tuple[List[Dict[str, Any]], int]:
    """把累积的一个小节文本切成 chunks(滑动窗口).

    Returns: (chunks, next_chunk_index)
    """
    text = "\n".join(buf_lines).strip()
    sid = _section_id(heading_path)
    if not text:
        return [], chunk_index_start

    pieces = _sliding_window(text, chunk_size, overlap)
    chunks = []
    idx = chunk_index_start
    for piece in pieces:
        chunks.append(
            {
                "content": piece,
                "content_length": len(piece),
                "heading_path": list(heading_path),
                "section_id": sid,
                "page_num": page_num,
                "chunk_type": "text",
                "chunk_index": idx,
            }
        )
        idx += 1
    return chunks, idx


def chunk_markdown(
    page_docs: List[Dict[str, Any]],
    chunk_size: int = 800,
    overlap: int = 64,
) -> List[Dict[str, Any]]:
    """按标题层级切分 Markdown(已分页),返回 chunks 列表.

    每个 chunk 含:content, content_length, heading_path, section_id, page_num, chunk_type, chunk_index
    表格作为独立 chunk(chunk_type=table),不参与标题切分。
    """
    chunks: List[Dict[str, Any]] = []
    heading_stack: List[Tuple[int, str]] = []  # [(level, title)]
    section_buf: List[str] = []
    table_buf: List[str] = []
    chunk_index = 0
    current_page = 1

    def current_heading_path() -> List[str]:
        return [t for _, t in heading_stack]

    def flush_section():
        nonlocal chunk_index, section_buf
        if not section_buf:
            return
        new_chunks, chunk_index = _flush_section(
            section_buf,
            current_heading_path(),
            current_page,
            chunk_index,
            chunk_size,
            overlap,
        )
        chunks.extend(new_chunks)
        section_buf = []

    def flush_table():
        nonlocal chunk_index, table_buf
        if not table_buf:
            return
        text = "\n".join(table_buf).strip()
        if text:
            sid = _section_id(current_heading_path() + ["__table__"])
            chunks.append(
                {
                    "content": text,
                    "content_length": len(text),
                    "heading_path": current_heading_path(),
                    "section_id": sid,
                    "page_num": current_page,
                    "chunk_type": "table",
                    "chunk_index": chunk_index,
                }
            )
            chunk_index += 1
        table_buf = []

    for page_doc in page_docs:
        # 兼容不同 pymupdf4llm 版本的字段
        page_num = 1
        text = ""
        if isinstance(page_doc, dict):
            meta = page_doc.get("metadata", {}) or {}
            # pymupdf4llm 用 page_number(1-indexed),兼容旧版 page/page_num
            page_num = int(meta.get("page_number", meta.get("page", meta.get("page_num", 1))))
            text = page_doc.get("text", page_doc.get("md", ""))
        elif isinstance(page_doc, str):
            text = page_doc
        current_page = page_num

        for line in text.split("\n"):
            # 表格行处理
            if _is_table_row(line) or _is_table_sep(line):
                # 表格前先 flush 文本 section
                flush_section()
                table_buf.append(line)
                continue
            else:
                # 非表格行,先 flush 表格
                if table_buf:
                    flush_table()

            level, title = _heading_level(line)
            if level > 0:
                # 遇到新标题,flush 当前 section
                flush_section()
                # 更新标题栈:弹出 level >= 当前的
                while heading_stack and heading_stack[-1][0] >= level:
                    heading_stack.pop()
                heading_stack.append((level, title))
            else:
                section_buf.append(line)

        # 每页结束 flush
        flush_table()
        flush_section()

    # 收尾
    flush_table()
    flush_section()

    logger.info("分块完成:共 %d 个 chunk", len(chunks))
    return chunks


def parse_pdf(
    pdf_path: str,
    chunk_size: int = 800,
    overlap: int = 64,
) -> Tuple[List[Dict[str, Any]], int]:
    """PDF → Markdown → 分块.

    Returns: (chunks, total_pages)
    """
    logger.info("解析 PDF: %s", pdf_path)
    # 用 pymupdf4llm 转 Markdown(保留表格),page_chunks=True 获取页码
    page_docs = pymupdf4llm.to_markdown(pdf_path, page_chunks=True)
    total_pages = len(page_docs) if page_docs else 0
    # 若 page_chunks 未返回页码,用 fitz 兜底拿页数
    if total_pages == 0:
        try:
            import fitz

            total_pages = fitz.open(pdf_path).page_count
        except Exception:
            total_pages = 0

    chunks = chunk_markdown(page_docs, chunk_size=chunk_size, overlap=overlap)
    return chunks, total_pages
