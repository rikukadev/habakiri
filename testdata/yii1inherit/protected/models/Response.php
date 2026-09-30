<?php
// アンケートごとに responses_<id> ができる(LimeSurvey 型)。テーブル族
abstract class Response extends Dynamic
{
    public function tableName()
    {
        return '{{responses_' . $this->surveyId . '}}';
    }
}
