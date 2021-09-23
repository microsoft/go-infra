using Azure;
using Azure.Storage.Blobs;
using Azure.Storage.Blobs.Models;
using Azure.Storage.Blobs.Specialized;
using Newtonsoft.Json;
using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Text;
using System.Threading.Tasks;

namespace GoLang.Mirror
{
    public sealed class PackageMirrorUtil
    {
        public BlobContainerClient ContainerClient { get; }
        public HttpClient HttpClient { get; }

        public PackageMirrorUtil(string connectionString, HttpClient? httpClient = null)
        {
            HttpClient = httpClient ?? new HttpClient();
            var client = new BlobServiceClient(connectionString);
            ContainerClient = client.GetBlobContainerClient(Constants.PackageContainerName);
        }

        public PackageMirrorUtil(BlobContainerClient containerClient, HttpClient? httpClient = null)
        {
            ContainerClient = containerClient;
            HttpClient = httpClient ?? new HttpClient();
        }

        public async Task MirrorPackageVersionAsync(string packageName, string version, bool overwrite = false)
        {
            var path = @$"{packageName}/@v/list";
            var blobClient = ContainerClient.GetBlobClient(path);
            await blobClient.CreateIfNotExistsAsync();

            if (!overwrite && await IsPackageMirroredAsync(packageName, version))
            {
                return;
            }

            await MirrorPackageContentAsync(packageName, version);
            var leaseClient = blobClient.GetBlobLeaseClient();
            var lease = await leaseClient.AcquireAsync(TimeSpan.FromSeconds(60));
            try
            {
                var versions = await GetMirroredPackageVersionsAsync(packageName);
                versions.Add(version);
                versions.Sort(StringComparer.Ordinal);

                var bytes = Encoding.UTF8.GetBytes(string.Join('\n', versions));
                var uploadStream = new MemoryStream(bytes);
                var uploadOptions = new BlobUploadOptions()
                {
                    Conditions = new BlobRequestConditions()
                    {
                        LeaseId = lease.Value.LeaseId,
                    }
                };
                await blobClient.UploadAsync(uploadStream, uploadOptions);
            }
            finally
            {
                await leaseClient.ReleaseAsync();
            }
        }

        public async Task<List<string>> GetMirroredPackageVersionsAsync(string packageName)
        {
            var path = @$"{packageName}/@v/list";
            var blobClient = ContainerClient.GetBlobClient(path);
            if (!await blobClient.ExistsAsync())
            {
                return new List<string>();
            }

            var list = new List<string>();
            using var stream = await blobClient.OpenReadAsync();
            return await ReadLinesAsync(stream);
        }

        public async Task<List<IndexPackageVersion>> GetUnmirroredIndexPackageVersionsAsync(DateTimeOffset? oldest = null, int limit = Constants.GoIndexPackageLimit)
        {
            var indexVersions = await GetIndexPackageVersionsAsync(oldest, limit);
            var list = new List<IndexPackageVersion>();
            foreach (var group in indexVersions.GroupBy(x => x.PackageName))
            {
                var versions = await GetMirroredPackageVersionsAsync(group.Key);
                foreach (var version in group)
                {
                    if (!versions.Contains(version.Version))
                    {
                        list.Add(version);
                    }
                }
            }

            return list;
        }

        public async Task<List<IndexPackageVersion>> GetIndexPackageVersionsAsync(DateTimeOffset? oldest = null, int limit = Constants.GoIndexPackageLimit)
        {
            var dt = oldest ?? (DateTimeOffset.UtcNow - TimeSpan.FromHours(1));
            var builder = new UriBuilder(Constants.GoIndexUri);
            builder.Path = "index";
            builder.Query = $"since={dt.ToRfc3339String()}&limit={limit}";
            var response = await GetProxySpecial(builder.Uri);
            using var stream = await response.Content.ReadAsStreamAsync();
            var lines =  await ReadLinesAsync(stream);
            var versions = new List<IndexPackageVersion>();
            foreach (var line in lines)
            {
                dynamic? obj = JsonConvert.DeserializeObject(line);
                if (obj is null)
                {
                    continue;
                }

                string packageName = obj.Path;
                string version = obj.Version;
                DateTimeOffset timestamp = DateTimeOffset.Parse((string)obj.Timestamp);
                versions.Add(new IndexPackageVersion(packageName, version, timestamp));
            }

            return versions;
        }

        public async Task<bool> IsPackageMirroredAsync(string packageName, string version)
        {
            var versions = await GetMirroredPackageVersionsAsync(packageName);
            return versions.Contains(version);
        }

        /// <summary>
        /// This will mirror the contents of a package (mod, zip and info file). This will overwrite the contents
        /// if they already exist
        /// </summary>
        public async Task MirrorPackageContentAsync(string packageName, string version, bool overwrite = false)
        {
            // The lock file is written when a task completes writing all of the 
            // package version content. If it is present then it's a sentinal that
            // the package content is correctly written.
            var lockPath = $"{packageName}/@v/{version}.lock";
            var lockContainerClient = ContainerClient.GetBlobClient(lockPath);
            if (!overwrite)
            {
                if (await lockContainerClient.ExistsAsync())
                {
                    return;
                }
            }
            else
            {
                await lockContainerClient.DeleteIfExistsAsync();
            }

            await MirrorOne($"{packageName}/@v/{version}.info");
            await MirrorOne($"{packageName}/@v/{version}.mod");
            await MirrorOne($"{packageName}/@v/{version}.zip");
            await lockContainerClient.CreateIfNotExistsAsync();
            async Task MirrorOne(string path)
            {
                var response = await GetProxySpecial(GetGoProxyUri(path));
                var stream = await response.Content.ReadAsStreamAsync();
                var blobClient = ContainerClient.GetBlobClient(path);
                await blobClient.UploadAsync(stream, overwrite: true);
            }
        }

        private static Uri GetGoProxyUri(string path)
        {
            var uriBuilder = new UriBuilder(Constants.GoProxyUri);
            uriBuilder.Path = path;
            return uriBuilder.Uri;
        }

        private static async Task<List<string>> ReadLinesAsync(Stream stream)
        {
            var list = new List<string>();
            var reader = new StreamReader(stream, Encoding.UTF8);
            while (await reader.ReadLineAsync() is { } line)
            {
                list.Add(line);
            }
            return list;
        }

        public Task<HttpResponseMessage> GetProxySpecial(Uri uri)
        {
            var message = new HttpRequestMessage(HttpMethod.Get, uri);
            message.Headers.Add("Disable-Module-Fetch", "true");
            return HttpClient.SendAsync(message);
        }
    }
}
