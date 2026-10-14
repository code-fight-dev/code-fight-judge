"use strict";

const fs = require("node:fs");
const path = require("node:path");
const ts = require("typescript");

const [sourcePath, outputPath] = process.argv.slice(2);
if (!sourcePath || !outputPath)
  throw new Error("source and output paths are required");
const javascript = sourcePath.endsWith(".js");
const source = fs.readFileSync(sourcePath, "utf8");
const syntax = ts.createSourceFile(
  sourcePath,
  source,
  ts.ScriptTarget.ES2022,
  true,
  javascript ? ts.ScriptKind.JS : ts.ScriptKind.TS,
);

function declaredFunctions(syntax) {
  const functions = new Map();
  for (const statement of syntax.statements) {
    if (ts.isFunctionDeclaration(statement) && statement.name)
      functions.set(statement.name.text, statement);
    if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (
          ts.isIdentifier(declaration.name) &&
          declaration.initializer &&
          (ts.isArrowFunction(declaration.initializer) ||
            ts.isFunctionExpression(declaration.initializer))
        )
          functions.set(declaration.name.text, declaration.initializer);
      }
    }
  }
  return functions;
}
const functions = declaredFunctions(syntax);
const entry = functions.has("Solve") ? "Solve" : "solve";
const fn = functions.get(entry);
if (!fn) {
  console.error(
    "Define a top-level function named Solve (or solve for the legacy string API).",
  );
  process.exit(1);
}
if (
  fn.parameters.some(
    (parameter) =>
      parameter.dotDotDotToken ||
      parameter.initializer ||
      parameter.questionToken,
  )
) {
  console.error(
    "Solve parameters must be required; rest, optional, and default parameters are not supported.",
  );
  process.exit(1);
}
const singleType =
  fn.parameters.length === 1 &&
  (fn.parameters[0].type ?? ts.getJSDocType(fn.parameters[0]));
const singleString = singleType?.kind === ts.SyntaxKind.StringKeyword;
const raw =
  singleString ||
  (javascript && entry === "solve" && fn.parameters.length === 1);
const bridge =
  `\n;module.exports.__cfEntry = ${entry};\n` +
  `module.exports.__cfArity = ${fn.parameters.length};\n` +
  `module.exports.__cfRaw = ${raw};\n` +
  `module.exports.__cfUntypedString = ${javascript && fn.parameters.length === 1};\n`;
const options = {
  target: ts.ScriptTarget.ES2022,
  module: ts.ModuleKind.CommonJS,
  moduleResolution: ts.ModuleResolutionKind.Node10,
  strict: true,
  allowJs: javascript,
  checkJs: false,
  noEmitOnError: true,
  skipLibCheck: true,
  esModuleInterop: true,
  types: ["node"],
  typeRoots: [
    path.dirname(path.dirname(require.resolve("@types/node/package.json"))),
  ],
  outDir: path.dirname(outputPath),
  rootDir: path.dirname(sourcePath),
};
const host = ts.createCompilerHost(options);
let resolvedRaw = raw;
const readFile = host.readFile.bind(host);
host.readFile = (file) =>
  path.resolve(file) === path.resolve(sourcePath)
    ? source + bridge
    : readFile(file);
// A submission may produce additional files through imports. Emit only its root
// program to the fixed output path; never honor user-controlled output options.
host.writeFile = (name, text, _bom, _error, files) => {
  if (
    files?.some(
      (file) => path.resolve(file.fileName) === path.resolve(sourcePath),
    ) &&
    name.endsWith(".js")
  )
    fs.writeFileSync(
      outputPath,
      text + `\nmodule.exports.__cfRaw = ${resolvedRaw};\n`,
    );
};
const program = ts.createProgram([sourcePath], options, host);
const diagnostics = ts.getPreEmitDiagnostics(program);
if (diagnostics.length) {
  console.error(
    ts.formatDiagnostics(diagnostics, {
      getCurrentDirectory: () => path.dirname(sourcePath),
      getCanonicalFileName: (file) => file,
      getNewLine: () => "\n",
    }),
  );
  process.exit(1);
}
const programEntry = declaredFunctions(program.getSourceFile(sourcePath)).get(
  entry,
);
if (programEntry.parameters.length === 1) {
  const type = program
    .getTypeChecker()
    .getTypeAtLocation(programEntry.parameters[0]);
  if (type.flags & (ts.TypeFlags.String | ts.TypeFlags.StringLiteral))
    resolvedRaw = true;
}
const emitted = program.emit();
if (emitted.emitSkipped || !fs.existsSync(outputPath)) process.exit(1);
