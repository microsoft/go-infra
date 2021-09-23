using Azure.Storage.Blobs;
using Azure.Storage.Blobs.Models;
using GoLang.Mirror;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Configuration.AzureKeyVault;
using System;
using System.Collections.Generic;
using System.Threading.Tasks;

namespace Scratch
{
    class Program
    {
        static async Task Main()
        {
            var configuration = CreateConfiguration();
            var connectionString = configuration[Constants.SecretNameStorageConnectionString];
            var packageMirrorUtil = new PackageMirrorUtil(connectionString);
            await FillTheMirror(packageMirrorUtil);

            var versions = await packageMirrorUtil.GetIndexPackageVersionsAsync();
            foreach (var version in versions)
            {
                await packageMirrorUtil.MirrorPackageVersionAsync(version.PackageName, version.Version);
            }

            await packageMirrorUtil.ContainerClient.CreateIfNotExistsAsync();
            await packageMirrorUtil.MirrorPackageVersionAsync("github.com/jaredpar/greetings", "v0.1.1");
            await packageMirrorUtil.MirrorPackageVersionAsync("github.com/jaredpar/greetings", "v0.1.1", overwrite: true);
            await packageMirrorUtil.MirrorPackageVersionAsync("github.com/jaredpar/greetings", "v0.1.1", overwrite: false);
        }

        internal static async Task FillTheMirror(PackageMirrorUtil packageMirrorUtil)
        {
            var delta = DateTimeOffset.UtcNow - TimeSpan.FromHours(1);
            while (true)
            {
                try
                {
                    var versions = await packageMirrorUtil.GetUnmirroredIndexPackageVersionsAsync(delta);
                    if (versions.Count == Constants.GoIndexPackageLimit)
                    {
                        Console.WriteLine($"Hit package limit, backing off");
                        delta = delta + TimeSpan.FromMinutes(15);
                        continue;
                    }

                    if (versions.Count == 0)
                    {
                        delta = delta - TimeSpan.FromHours(0.5);
                        Console.WriteLine($"Moving back to {delta.ToRfc3339String()}");
                        continue;
                    }

                    var list = new List<Task>();
                    foreach (var version in versions)
                    {
                        Console.WriteLine($"Mirroring {version.PackageName} {version.Version}");
                        list.Add(packageMirrorUtil.MirrorPackageVersionAsync(version.PackageName, version.Version));
                    }
                    await Task.WhenAll(list);

                    Console.WriteLine($"Mirrored {versions.Count} packages");
                }
                catch (Exception ex)
                {
                    Console.WriteLine(ex.Message);
                }
            }


        }

        internal static IConfiguration CreateConfiguration()
        {
            var config = new ConfigurationBuilder()
                .AddAzureKeyVault(
                    "https://golangexp2.vault.azure.net/",
                    new DefaultKeyVaultSecretManager())
                .Build();
            return config;
        }
    }
}
