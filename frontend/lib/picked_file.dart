import 'dart:async';
import 'dart:js_interop';

import 'package:web/web.dart';

/// A user-selected local file that keeps the browser [File] handle.
///
/// We must not read the whole file into Dart (`Uint8List`) up front: that would
/// freeze the UI on large files. Chunk uploads should [File.slice] and send the
/// resulting [Blob] directly.
class PickedLocalFile {
  PickedLocalFile({
    required this.name,
    required this.size,
    required this.file,
  });

  final String name;
  final int size;
  final File file;
}

Future<PickedLocalFile?> pickLocalFile() {
  final input = HTMLInputElement()
    ..type = 'file'
    ..multiple = false
    ..style.display = 'none';
  final completer = Completer<PickedLocalFile?>();
  var settled = false;

  void finish(PickedLocalFile? value) {
    if (settled) return;
    settled = true;
    input.remove();
    completer.complete(value);
  }

  input.addEventListener(
    'change',
    (Event _) {
      final file = input.files?.item(0);
      if (file == null) {
        finish(null);
        return;
      }
      finish(PickedLocalFile(name: file.name, size: file.size, file: file));
    }.toJS,
  );
  input.addEventListener(
    'cancel',
    (Event _) {
      finish(null);
    }.toJS,
  );

  document.body!.append(input);
  input.click();
  return completer.future;
}
