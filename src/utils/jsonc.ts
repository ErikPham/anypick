/** Parse JSONC without changing string contents such as URLs or comment-like model ids. */
export function parseJsonc(text: string): unknown {
  return JSON.parse(stripTrailingCommas(stripComments(text)));
}

function stripComments(text: string): string {
  let out = '';
  let inString = false;
  let escaped = false;
  let lineComment = false;
  let blockComment = false;

  for (let index = 0; index < text.length; index += 1) {
    const char = text[index] ?? '';
    const next = text[index + 1] ?? '';
    if (lineComment) {
      if (char === '\n' || char === '\r') {
        lineComment = false;
        out += char;
      } else {
        out += ' ';
      }
      continue;
    }
    if (blockComment) {
      if (char === '*' && next === '/') {
        blockComment = false;
        out += '  ';
        index += 1;
      } else {
        out += char === '\n' || char === '\r' ? char : ' ';
      }
      continue;
    }
    if (inString) {
      out += char;
      if (escaped) {
        escaped = false;
      } else if (char === '\\') {
        escaped = true;
      } else if (char === '"') {
        inString = false;
      }
      continue;
    }
    if (char === '"') {
      inString = true;
      out += char;
    } else if (char === '/' && next === '/') {
      lineComment = true;
      out += '  ';
      index += 1;
    } else if (char === '/' && next === '*') {
      blockComment = true;
      out += '  ';
      index += 1;
    } else {
      out += char;
    }
  }
  return out;
}

function stripTrailingCommas(text: string): string {
  let out = '';
  let inString = false;
  let escaped = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index] ?? '';
    if (inString) {
      out += char;
      if (escaped) {
        escaped = false;
      } else if (char === '\\') {
        escaped = true;
      } else if (char === '"') {
        inString = false;
      }
      continue;
    }
    if (char === '"') {
      inString = true;
      out += char;
      continue;
    }
    if (char === ',') {
      let lookahead = index + 1;
      while (/\s/u.test(text[lookahead] ?? '')) {
        lookahead += 1;
      }
      if (text[lookahead] === '}' || text[lookahead] === ']') {
        out += ' ';
        continue;
      }
    }
    out += char;
  }
  return out;
}
