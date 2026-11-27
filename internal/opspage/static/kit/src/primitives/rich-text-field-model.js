// Read a bounded DOM tree as rich text. Never return or move a node from the source.
import { parseLinkURL } from '../render/validate.js';
import { RICH_TEXT_EDIT_LIMIT, RICH_TEXT_MAX_BLOCKS, richHeadingLevel, richNode, richTextLinkSchemes,
  richTextValueErrors } from '../render/rich-text.js';

const HTML = 'http://www.w3.org/1999/xhtml';
const MAX_SOURCE = 262144;
const MAX_NODES = 12000;
const MAX_DEPTH = 32;
const TOO_LARGE = 'The edit is too large. Use less text or formatting.';
export const INLINE_MARKS = Object.freeze({ emphasis: 'em', strong: 'strong', code: 'code' });
const MARK = new Map([['em', 'emphasis'], ['i', 'emphasis'], ['strong', 'strong'], ['b', 'strong'], ['code', 'code']]);
const BLOCK = new Set(['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'blockquote', 'pre']);
const GROUP = new Set(['div', 'article', 'section', 'main', 'header', 'footer', 'aside', 'address', 'figure', 'figcaption']);
const INLINE = new Set(['span', 'font', 'small', 'big', 's', 'strike', 'u', 'sub', 'sup', 'abbr', 'cite', 'q',
  'kbd', 'samp', 'var', 'mark', 'bdi', 'bdo', 'time', 'del', 'ins']);
const SAFE = new Set([...BLOCK, ...GROUP, ...INLINE, ...MARK.keys(), 'a', 'br']);

export const richSpan = (text, mark) => richNode('rich-span', mark ? { text, mark } : { text });
export const richParagraph = (content) => richNode('rich-paragraph', { content });
export const richValue = (blocks) => richNode('rich-text', { blocks });

function childrenOf(root, editorLink, ignoreText) {
  let count = 0;
  let size = 0;
  let tooLarge = false;

  function walk(parent, depth, keep) {
    const result = [];
    for (let node = parent.firstChild; node && !tooLarge; node = node.nextSibling) {
      count += 1;
      if (count > MAX_NODES || depth > MAX_DEPTH) {
        tooLarge = true;
        break;
      }
      if (node.nodeType === 3) {
        size += node.data.length;
        if (size > MAX_SOURCE && !editorLink) tooLarge = true;
        if (keep && node.data) result.push({ text: node.data });
        else if (node.data) ignoreText?.(node);
        continue;
      }
      const name = node.namespaceURI === HTML ? node.localName : '';
      const allowed = keep && node.nodeType === 1 && SAFE.has(name);
      const children = walk(node, depth + 1, allowed);
      if (name === 'template' && node.content) walk(node.content, depth + 1, false);
      if (!allowed || tooLarge) continue;
      const href = name === 'a' ? (editorLink?.(node) ?? node.getAttribute('href')) : null;
      result.push({ name, children, href });
    }
    return result;
  }

  const children = walk(root, 1, true);
  return tooLarge ? null : children;
}

function words(nodes) {
  return nodes.map((node) => node.text ?? (node.name === 'br' ? '\n' : words(node.children))).join('');
}

function inline(nodes, schemes, mark) {
  const content = [];
  for (const node of nodes) {
    if (node.text !== undefined) {
      content.push(richSpan(node.text, mark));
    } else if (node.name === 'br') {
      content.push(richSpan('\n', mark));
    } else if (node.name === 'a') {
      const label = words(node.children);
      if (label) {
        const url = parseLinkURL(node.href, schemes);
        content.push(url ? richNode('rich-link', { text: label, url: node.href }) : richSpan(label));
      }
    } else {
      content.push(...inline(node.children, schemes, MARK.get(node.name) ?? mark));
    }
  }
  return content;
}

function blocksOf(nodes, schemes, imported) {
  const blocks = [];
  let level = 0;
  const contentOf = (nodes) => !imported && nodes.length === 1 && nodes[0].name === 'br' ? [] : inline(nodes, schemes);

  function walk(nodes) {
    let pending = [];
    const flush = () => {
      if (pending.some((node) => node.name || node.text.trim())) blocks.push(richParagraph(contentOf(pending)));
      pending = [];
    };
    for (const node of nodes) {
      const { name, children } = node;
      if (GROUP.has(name) || BLOCK.has(name)) {
        flush();
        if (GROUP.has(name) && children.some(({ name }) => BLOCK.has(name) || GROUP.has(name))) walk(children);
        else if (name?.startsWith('h') && BLOCK.has(name)) {
          const next = Number(name[1]);
          level = richHeadingLevel(next, level);
          blocks.push(richNode('rich-heading', { level, content: contentOf(children) }));
        } else if (name === 'ul' || name === 'ol') {
          const items = children.filter(({ name }) => name === 'li').map(({ children }) => richParagraph(contentOf(children)));
          blocks.push(richNode('rich-list', name === 'ol' ? { ordered: true, items } : { items }));
        } else if (name === 'blockquote') {
          const containsParagraphs = children.some(({ name }) => name === 'p');
          const paragraphs = containsParagraphs
            ? children.filter(({ name }) => name === 'p').map(({ children }) => richParagraph(contentOf(children)))
            : children.length ? [richParagraph(contentOf(children))] : [];
          blocks.push(richNode('rich-quote', { paragraphs }));
        } else if (name === 'pre') {
          const code = words(children);
          blocks.push(code ? richNode('rich-code', { text: code }) : richParagraph([]));
        } else blocks.push(richParagraph(contentOf(children)));
      } else pending.push(node);
    }
    flush();
  }
  walk(nodes);
  return blocks;
}

function checked(value) {
  return richTextValueErrors(value).length ? { problem: RICH_TEXT_EDIT_LIMIT } : { value };
}

export function readRichTree(root, { imported = false, editorLink, ignoreText } = {}) {
  const tree = childrenOf(root, editorLink, ignoreText);
  return tree === null ? { problem: TOO_LARGE } : checked(richValue(blocksOf(tree, richTextLinkSchemes(), imported)));
}

function parseHTML(source) {
  return new DOMParser().parseFromString(source, 'text/html').body;
}

export function readRichTransfer(transfer, parse = parseHTML) {
  const source = transfer?.getData('text/html') ?? '';
  if (source.length > MAX_SOURCE) return { problem: TOO_LARGE };
  if (source) {
    let root;
    try {
      root = parse(source);
    } catch {
      // A page policy can refuse the inert parser. Try the plain text from the same transfer.
    }
    if (root) {
      const result = readRichTree(root, { imported: true });
      if (result.problem || result.value.data.blocks.length) return result;
    }
  }
  const plain = transfer?.getData('text/plain') ?? '';
  if (plain.length > MAX_SOURCE) return { problem: TOO_LARGE };
  const lines = plain ? plain.split(/\r\n?|\n/, RICH_TEXT_MAX_BLOCKS + 1) : [];
  if (lines.length > RICH_TEXT_MAX_BLOCKS) return { problem: TOO_LARGE };
  return checked(richValue(lines.map((line) => richParagraph(line ? [richSpan(line)] : []))));
}
