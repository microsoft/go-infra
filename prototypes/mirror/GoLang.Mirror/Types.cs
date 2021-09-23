using System;
using System.Collections.Generic;
using System.Text;

namespace GoLang.Mirror
{
    public sealed record class IndexPackageVersion(
        string PackageName,
        string Version,
        DateTimeOffset Timestamp);
}
