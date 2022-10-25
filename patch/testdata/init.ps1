# Copyright (c) Microsoft Corporation.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# Initialize the dev setup to modify moremath.pack or the testing patches.
. .\clean.ps1
New-Item -Type Directory patch-dev

git clone moremath.pack moremath
