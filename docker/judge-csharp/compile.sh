#!/bin/sh
set -eu

set -- /usr/share/dotnet/sdk/*/Roslyn/bincore/csc.dll
exec dotnet "$1" -nologo -noconfig -nostdlib+ -langversion:12.0 -target:exe \
    -out:/output/Submission.dll @/opt/judge-csharp/references.rsp /workspace/Program.cs
