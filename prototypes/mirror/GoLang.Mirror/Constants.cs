using System;
using System.Collections.Generic;
using System.Text;

namespace GoLang.Mirror
{
    public static class Constants
    {
        public const string SecretNameStorageConnectionString = "StorageConnectionString";

        public const string QueueNameMirrorPackageVersion = "mirror-package-version";

        public const string PackageContainerName = "packages";
        public const string GoProxyUri = "https://proxy.golang.org/";
        public const string GoIndexUri = "https://index.golang.org/";
        public const int GoIndexPackageLimit = 2_000;
        public const string KeyVaultUri = "https://golangexp2.vault.azure.net/";
    }
}
