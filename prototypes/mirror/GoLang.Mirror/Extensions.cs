using Azure.Storage.Blobs;
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Threading.Tasks;

namespace GoLang.Mirror
{
    public static class Extensions
    {
        public static async Task CreateIfNotExistsAsync(this BlobClient blobClient)
        {
            if (await blobClient.ExistsAsync())
            {
                return;
            }

            var stream = new MemoryStream();
            await blobClient.UploadAsync(stream);
        }

        public static string ToRfc3339String(this DateTimeOffset dateTime) =>
            dateTime.UtcDateTime.ToString("yyyy-MM-ddTHH:mm:ssZ");
    }
}
