<?php
// レコードの種類ごとに responses_<id> ができる設計。テーブル族
abstract class Response extends Dynamic
{
    public function tableName()
    {
        return '{{responses_' . $this->formId . '}}';
    }
}
