"use strict";

const fs = require("node:fs");

function jsonArguments(input) {
  const values = [];
  let position = 0;
  while (position < input.length) {
    while (/[ \r\n\t]/.test(input[position] ?? "") && position < input.length)
      position++;
    if (position === input.length) break;
    const start = position;
    const first = input[position];
    if (first === "{" || first === "[" || first === '"') {
      let depth = 0,
        quoted = false,
        escaped = false,
        finished = false;
      for (; position < input.length; position++) {
        const character = input[position];
        if (quoted) {
          if (escaped) escaped = false;
          else if (character === "\\") escaped = true;
          else if (character === '"') {
            quoted = false;
            if (first === '"' && depth === 0) {
              position++;
              finished = true;
              break;
            }
          }
        } else if (character === '"') quoted = true;
        else if (character === "{" || character === "[") depth++;
        else if (character === "}" || character === "]") {
          if (--depth === 0) {
            position++;
            finished = true;
            break;
          }
        }
      }
      if (!finished) throw new Error("unterminated JSON argument");
    } else {
      const scalar =
        /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(
          input.slice(position),
        );
      if (!scalar) throw new Error("invalid JSON argument");
      position += scalar[0].length;
    }
    values.push(
      JSON.parse(input.slice(start, position), (_key, value) => {
        if (
          typeof value === "number" &&
          (!Number.isFinite(value) ||
            (Number.isInteger(value) && !Number.isSafeInteger(value)))
        )
          throw new Error(
            "numeric input exceeds JavaScript's safe number range",
          );
        return value;
      }),
    );
  }
  return values;
}

function rawString(input) {
  try {
    const decoded = JSON.parse(input);
    if (typeof decoded === "string") return decoded;
  } catch {}
  return input.replace(/[\r\n]+$/, "");
}

async function main() {
  const input = fs.readFileSync(0, "utf8");
  const solution = require(process.argv[2]);
  if (typeof solution.__cfEntry !== "function")
    throw new Error("Solve is not a function");
  let args;
  if (solution.__cfRaw) args = [rawString(input)];
  else {
    try {
      args = jsonArguments(input);
    } catch (error) {
      if (!solution.__cfUntypedString || /^[\s]*[\[{"\d-]/.test(input))
        throw error;
      args = [rawString(input)];
    }
  }
  if (args.length !== solution.__cfArity)
    throw new Error(
      `expected ${solution.__cfArity} input arguments, received ${args.length}`,
    );
  const result = await solution.__cfEntry(...args);
  if (typeof result === "string") process.stdout.write(result);
  else {
    const output = JSON.stringify(result, (_key, value) => {
      if (typeof value === "number" && !Number.isFinite(value))
        throw new Error("Solve returned a non-finite number");
      return value;
    });
    if (output === undefined)
      throw new Error("Solve must return one JSON value or string");
    process.stdout.write(output);
  }
}
const keepAlive = setInterval(() => {}, 1000);
let completed = false;
process.on("exit", (code) => {
  if (!completed && code === 0) {
    console.error("Solve exited before completing");
    process.exitCode = 1;
  }
});
main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
}).finally(() => {
  completed = true;
  clearInterval(keepAlive);
});
